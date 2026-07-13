package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// lbEndpoint is a single barge/server backend registered with a TunnelTug LB.
type lbEndpoint struct {
	Host        string
	ControlPort string
	PublicPort  string
	Namespace   string
	FleetID     string
}

type bargeLBRegistrar struct {
	baseURL    string
	httpClient *http.Client
	fleetID    string
}

func newBargeLBRegistrar(lbAddr string) (*bargeLBRegistrar, error) {
	addr := strings.TrimSpace(lbAddr)
	if addr == "" {
		return nil, fmt.Errorf("-barge-lb address is required")
	}
	if !strings.Contains(addr, ":") {
		return nil, fmt.Errorf("invalid -barge-lb %q: use host:port", lbAddr)
	}

	scheme := "http"
	if *prod || *dev || *backendInsecure {
		// LB public listeners run with production TLS; -backend-insecure is the
		// local registration path without enabling -prod on child barge servers.
		scheme = "https"
	}

	transport := &http.Transport{
		Proxy:               nil,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if scheme == "https" {
		cfg := backendTLSConfig()
		cfg.InsecureSkipVerify = *insecure || *backendInsecure
		transport.TLSClientConfig = cfg
	}

	return &bargeLBRegistrar{
		baseURL: scheme + "://" + addr,
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		fleetID: strings.TrimSpace(*bargeFleetID),
	}, nil
}

func (r *bargeLBRegistrar) endpointFromBarge(host string, b *bargeInstance) lbEndpoint {
	return lbEndpoint{
		Host:        host,
		ControlPort: b.controlPort,
		PublicPort:  b.publicPort,
		Namespace:   normalizeNamespace(*namespace),
		FleetID:     r.fleetLabel(b),
	}
}

func (r *bargeLBRegistrar) registerRequest(ep lbEndpoint) lbRegisterRequest {
	fleet := strings.TrimSpace(ep.FleetID)
	if fleet == "" {
		fleet = strings.TrimSpace(r.fleetID)
	}
	ns := strings.TrimSpace(ep.Namespace)
	if ns == "" {
		ns = normalizeNamespace(*namespace)
	}
	return lbRegisterRequest{
		Token:       strings.TrimSpace(*authToken),
		Host:        ep.Host,
		ControlPort: ep.ControlPort,
		PublicPort:  ep.PublicPort,
		Namespace:   ns,
		FleetID:     fleet,
	}
}

func (r *bargeLBRegistrar) fleetLabel(b *bargeInstance) string {
	if r.fleetID != "" {
		return fmt.Sprintf("%s-%d", r.fleetID, b.id)
	}
	return fmt.Sprintf("barge-%d", b.id)
}

func (r *bargeLBRegistrar) waitReady(ctx context.Context, ep lbEndpoint) error {
	target := r.publicBaseURL(ep) + "/_tunneltug/health"
	deadline := time.Now().Add(30 * time.Second)

	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		resp, err := r.httpClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("health check timed out: %w", err)
			}
			return fmt.Errorf("health check timed out")
		}
		if !sleepOrDone(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func (r *bargeLBRegistrar) register(ctx context.Context, ep lbEndpoint) error {
	return r.postAction(ctx, "/_tunneltug/lb/register", r.registerRequest(ep))
}

func (r *bargeLBRegistrar) deregister(ctx context.Context, ep lbEndpoint) error {
	return r.postAction(ctx, "/_tunneltug/lb/deregister", r.registerRequest(ep))
}

func (r *bargeLBRegistrar) heartbeat(ctx context.Context, ep lbEndpoint) {
	interval := time.Duration(*bargeLBHeartbeat) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.postAction(ctx, "/_tunneltug/lb/heartbeat", r.registerRequest(ep)); err != nil && !*quiet {
				log.Printf("LB heartbeat failed for %s: %v", net.JoinHostPort(ep.Host, ep.ControlPort), err)
			}
		}
	}
}

// runRegistrationLifecycle waits for health, registers, heartbeats until ctx is done, then deregisters.
func (r *bargeLBRegistrar) runRegistrationLifecycle(ctx context.Context, ep lbEndpoint, label string) {
	if err := r.waitReady(ctx, ep); err != nil {
		if ctx.Err() == nil && !*quiet {
			log.Printf("%s not ready for LB registration: %v", label, err)
		}
		return
	}
	if err := r.register(ctx, ep); err != nil {
		if ctx.Err() == nil && !*quiet {
			log.Printf("%s LB registration failed: %v", label, err)
		}
		return
	}
	if !*quiet {
		log.Printf("%s registered with LB at %s", label, r.baseURL)
	}

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go r.heartbeat(hbCtx, ep)

	<-ctx.Done()
	hbCancel()
	deregCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.deregister(deregCtx, ep); err != nil && !*quiet {
		log.Printf("%s LB deregistration failed: %v", label, err)
	}
}

func (r *bargeLBRegistrar) postAction(ctx context.Context, path string, payload lbRegisterRequest) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed lbRegisterResponse
		if json.Unmarshal(respBody, &parsed) == nil && parsed.Error != "" {
			return fmt.Errorf("%s", parsed.Error)
		}
		return fmt.Errorf("LB returned status %d", resp.StatusCode)
	}
	return nil
}

func (r *bargeLBRegistrar) publicBaseURL(ep lbEndpoint) string {
	scheme := "http"
	if *prod || *dev {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(ep.Host, ep.PublicPort)
}
