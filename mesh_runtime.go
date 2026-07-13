package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0TrustCloud/mesh_client"
)

type meshSettings struct {
	Platform      string
	GatewayAddr   string
	GatewayPub    string
	HostID        string
	RegisterURL   string
	RegisterToken string
	DataDir       string
}

func meshActive() bool {
	return *meshEnabled
}

func vpiActive() bool {
	return *vpiStub || *meshEnabled || dnsConfigActive()
}

func platformMeshJoinActive() bool {
	return meshActive() && (*meshJoinPlatform || strings.TrimSpace(*meshGateway) != "" || strings.TrimSpace(*meshPubkey) != "")
}

func resolveMeshSettings() (meshSettings, error) {
	out := meshSettings{
		Platform:      strings.TrimRight(strings.TrimSpace(*meshPlatform), "/"),
		GatewayAddr:   strings.TrimSpace(*meshGateway),
		GatewayPub:    strings.TrimSpace(*meshPubkey),
		HostID:        meshHostID(),
		RegisterURL:   strings.TrimSpace(*meshRegisterURL),
		RegisterToken: strings.TrimSpace(*authToken),
		DataDir:       strings.TrimSpace(*meshDataDir),
	}
	if out.Platform == "" {
		out.Platform = "https://0trust.cloud"
	}
	if out.HostID == "" {
		out.HostID = "direct"
	}
	if out.RegisterURL == "" {
		out.RegisterURL = out.Platform + "/api/v1/access/register-mesh"
	}
	if out.DataDir == "" {
		home, _ := os.UserHomeDir()
		out.DataDir = filepath.Join(home, ".tunneltug", "mesh", "platform", out.HostID)
	}
	if out.GatewayAddr == "" || out.GatewayPub == "" {
		addr, pub, err := fetchPlatformMesh(out.Platform)
		if err != nil {
			return out, err
		}
		if out.GatewayAddr == "" {
			out.GatewayAddr = addr
		}
		if out.GatewayPub == "" {
			out.GatewayPub = pub
		}
	}
	if out.GatewayAddr == "" || out.GatewayPub == "" {
		return out, fmt.Errorf("mesh gateway not configured (set -mesh-gateway/-mesh-pubkey or check -mesh-platform)")
	}
	return out, nil
}

func fetchPlatformMesh(platformURL string) (gatewayAddr, gatewayPub string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	resp, err := client.Get(platformURL + "/api/v1/platform")
	if err != nil {
		return "", "", fmt.Errorf("platform mesh bootstrap: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("platform mesh bootstrap HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var payload struct {
		Mesh struct {
			GatewayAddr   string `json:"gateway_addr"`
			GatewayPubKey string `json:"gateway_pubkey"`
		} `json:"mesh"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", "", fmt.Errorf("decode platform mesh: %w", err)
	}
	return strings.TrimSpace(payload.Mesh.GatewayAddr), strings.TrimSpace(payload.Mesh.GatewayPubKey), nil
}

// startMeshRuntime runs client-side mesh join: register with the built-in server
// authority, and optionally join an external 0Trust platform mesh.
func startMeshRuntime(ctx context.Context) {
	if !meshActive() {
		return
	}
	modeVal := strings.ToLower(strings.TrimSpace(*mode))
	if modeVal == "client" {
		go registerWithBuiltInMesh(ctx)
	}
	if platformMeshJoinActive() {
		startPlatformMeshRuntime(ctx)
	}
}

func registerWithBuiltInMesh(ctx context.Context) {
	hostID := meshHostID()
	privateName := meshPrivateName(hostID)
	backoff := time.Second
	for ctx.Err() == nil {
		if err := postBuiltInMeshRegister(hostID); err != nil {
			if !*quiet {
				log.Printf("[mesh] register %s: %v — retry in %s", privateName, err, backoff)
			}
			if !sleepOrDone(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		log.Printf("[mesh] published private name %s (built-in secure_dns/registrar)", privateName)
		// Refresh registration periodically so A records stay fresh.
		if !sleepOrDone(ctx, 30*time.Second) {
			return
		}
		backoff = time.Second
	}
}

func postBuiltInMeshRegister(hostID string) error {
	base := meshRegistryBaseURL()
	body, _ := json.Marshal(map[string]string{
		"host_id": hostID,
		"edge_ip": strings.TrimSpace(*meshEdgeIP),
	})
	req, err := http.NewRequest(http.MethodPost, base+"/_tunneltug/mesh/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tunnel-Token", strings.TrimSpace(*authToken))
	client := &http.Client{Timeout: 15 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s (%s)", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func meshRegistryBaseURL() string {
	// Prefer the same public origin the tunnel uses.
	scheme := publicScheme()
	if !*prod && !*dev {
		// Dev HTTP public port on server.
		host := strings.TrimSpace(*serverIP)
		if host == "" {
			host = "127.0.0.1"
		}
		port := strings.TrimSpace(*publicPort)
		if port == "" || port == "8080" {
			// Clients often talk to control host; try common public ports.
			return fmt.Sprintf("http://%s:%s", host, firstNonEmpty(port, "8080"))
		}
		return fmt.Sprintf("http://%s:%s", host, port)
	}
	host := strings.TrimSpace(*domain)
	if host == "" {
		host = strings.TrimSpace(*serverIP)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	port := strings.TrimSpace(*publicPort)
	defaultPort := "443"
	if scheme == "http" {
		defaultPort = "80"
	}
	if port == "" || port == defaultPort {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

func startPlatformMeshRuntime(ctx context.Context) {
	cfg, err := resolveMeshSettings()
	if err != nil {
		log.Printf("[mesh] platform join disabled: %v", err)
		return
	}
	pub, err := mesh_client.DecodeGatewayPubKey(cfg.GatewayPub)
	if err != nil {
		log.Printf("[mesh] invalid gateway pubkey: %v", err)
		return
	}

	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			rt, err := mesh_client.Bootstrap(ctx, mesh_client.Options{
				DataDir:       cfg.DataDir,
				GatewayAddr:   cfg.GatewayAddr,
				GatewayPubKey: pub,
				ServiceName:   "tunneltug_" + cfg.HostID,
				HostID:        cfg.HostID,
				Connect:       true,
				ServerMode:    true,
			})
			if err != nil {
				log.Printf("[mesh] platform bootstrap failed: %v — retry in %s", err, backoff)
				if !sleepOrDone(ctx, backoff) {
					return
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			backoff = time.Second
			log.Printf("[mesh] platform connected — host_id=%s private_name=%s", cfg.HostID, meshPrivateName(cfg.HostID))

			if err := registerMeshPeer(cfg, rt.Node.GetNoisePubKey()); err != nil {
				log.Printf("[mesh] platform register peer: %v", err)
			}

			ticker := time.NewTicker(2 * time.Second)
			for ctx.Err() == nil {
				if !rt.Node.Connected() {
					ticker.Stop()
					rt.Close()
					log.Printf("[mesh] platform disconnected — reconnecting")
					break
				}
				<-ticker.C
			}
		}
	}()
}

func registerMeshPeer(cfg meshSettings, pubKey []byte) error {
	body := map[string]string{
		"host_id": cfg.HostID,
		"pubkey":  hex.EncodeToString(pubKey),
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, cfg.RegisterURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tunnel-Token", cfg.RegisterToken)
	client := &http.Client{Timeout: 15 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register mesh: %s (%s)", resp.Status, strings.TrimSpace(string(b)))
	}
	log.Printf("[mesh] registered endpoint %s with external platform", cfg.HostID)
	return nil
}

// publishTunnelToMesh is called by the server when a control tunnel comes online.
func publishTunnelToMesh(namespace, subdomain string) {
	auth := getGlobalMeshAuthority()
	if auth == nil {
		return
	}
	hostID := strings.ToLower(strings.TrimSpace(subdomain))
	if isDirectRouting() {
		hostID = "direct"
	}
	if hostID == "" {
		hostID = defaultTunnelKey
	}
	ns := normalizeNamespace(namespace)
	if ns != defaultNamespace {
		hostID = hostID + "-" + ns
	}
	name, err := auth.PublishTunnel(hostID, auth.edgeIP)
	if err != nil {
		log.Printf("[mesh] publish tunnel %s: %v", hostID, err)
		return
	}
	if !*quiet {
		log.Printf("[mesh] zone record %s → %s", name, auth.edgeIP)
	}
}

func unpublishTunnelFromMesh(namespace, subdomain string) {
	auth := getGlobalMeshAuthority()
	if auth == nil {
		return
	}
	hostID := strings.ToLower(strings.TrimSpace(subdomain))
	if isDirectRouting() {
		hostID = "direct"
	}
	if hostID == "" {
		hostID = defaultTunnelKey
	}
	ns := normalizeNamespace(namespace)
	if ns != defaultNamespace {
		hostID = hostID + "-" + ns
	}
	auth.UnpublishTunnel(hostID)
}
