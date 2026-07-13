package main

import (
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

type lbRegisterRequest struct {
	Token       string `json:"token"`
	Host        string `json:"host"`
	ControlPort string `json:"control_port"`
	PublicPort  string `json:"public_port"`
	Namespace   string `json:"namespace,omitempty"`
	FleetID     string `json:"fleet_id,omitempty"`
}

type lbRegisterResponse struct {
	Status    string `json:"status"`
	BackendID string `json:"backend_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (m *LBManager) mountRegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/_tunneltug/lb/register", m.handleLBRegister)
	mux.HandleFunc("/_tunneltug/lb/deregister", m.handleLBDeregister)
	mux.HandleFunc("/_tunneltug/lb/heartbeat", m.handleLBHeartbeat)
}

func (m *LBManager) handleLBRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := decodeLBRegisterRequest(r.Body)
	if err != nil {
		writeLBRegisterResponse(w, http.StatusBadRequest, lbRegisterResponse{Error: err.Error()})
		return
	}
	if !tokensEqual(req.Token, *authToken) {
		writeLBRegisterResponse(w, http.StatusUnauthorized, lbRegisterResponse{Error: "unauthorized"})
		return
	}
	backend, err := m.registerDynamicBackend(req)
	if err != nil {
		writeLBRegisterResponse(w, http.StatusBadRequest, lbRegisterResponse{Error: err.Error()})
		return
	}
	if !*quiet {
		log.Printf("LB registered barge backend %s (public %s, fleet %s)", backend.id, backend.publicAddr(), req.FleetID)
	}
	writeLBRegisterResponse(w, http.StatusOK, lbRegisterResponse{Status: "ok", BackendID: backend.id})
}

func (m *LBManager) handleLBDeregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := decodeLBRegisterRequest(r.Body)
	if err != nil {
		writeLBRegisterResponse(w, http.StatusBadRequest, lbRegisterResponse{Error: err.Error()})
		return
	}
	if !tokensEqual(req.Token, *authToken) {
		writeLBRegisterResponse(w, http.StatusUnauthorized, lbRegisterResponse{Error: "unauthorized"})
		return
	}
	id, err := backendIDFromRegisterRequest(req)
	if err != nil {
		writeLBRegisterResponse(w, http.StatusBadRequest, lbRegisterResponse{Error: err.Error()})
		return
	}
	if m.deregisterDynamicBackend(id) {
		if !*quiet {
			log.Printf("LB deregistered barge backend %s", id)
		}
	}
	writeLBRegisterResponse(w, http.StatusOK, lbRegisterResponse{Status: "ok", BackendID: id})
}

func (m *LBManager) handleLBHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := decodeLBRegisterRequest(r.Body)
	if err != nil {
		writeLBRegisterResponse(w, http.StatusBadRequest, lbRegisterResponse{Error: err.Error()})
		return
	}
	if !tokensEqual(req.Token, *authToken) {
		writeLBRegisterResponse(w, http.StatusUnauthorized, lbRegisterResponse{Error: "unauthorized"})
		return
	}
	id, err := backendIDFromRegisterRequest(req)
	if err != nil {
		writeLBRegisterResponse(w, http.StatusBadRequest, lbRegisterResponse{Error: err.Error()})
		return
	}
	if !m.touchDynamicBackend(id) {
		backend, regErr := m.registerDynamicBackend(req)
		if regErr != nil {
			writeLBRegisterResponse(w, http.StatusNotFound, lbRegisterResponse{Error: "backend not registered"})
			return
		}
		id = backend.id
	}
	writeLBRegisterResponse(w, http.StatusOK, lbRegisterResponse{Status: "ok", BackendID: id})
}

func decodeLBRegisterRequest(body io.Reader) (lbRegisterRequest, error) {
	var req lbRegisterRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return req, fmt.Errorf("invalid JSON body")
	}
	return req, nil
}

func backendIDFromRegisterRequest(req lbRegisterRequest) (string, error) {
	host := strings.TrimSpace(req.Host)
	controlPort := strings.TrimSpace(req.ControlPort)
	if host == "" || controlPort == "" {
		return "", fmt.Errorf("host and control_port are required")
	}
	return net.JoinHostPort(host, controlPort), nil
}

func backendFromRegisterRequest(req lbRegisterRequest) (*tunnelBackend, error) {
	host := strings.TrimSpace(req.Host)
	ctrlPort := strings.TrimSpace(req.ControlPort)
	pubPort := strings.TrimSpace(req.PublicPort)

	if host == "" {
		return nil, fmt.Errorf("host is required")
	}
	if ctrlPort == "" {
		ctrlPort = strings.TrimSpace(*controlPort)
	}
	if pubPort == "" {
		pubPort = strings.TrimSpace(*publicPort)
	}
	if err := validatePort("register-control", ctrlPort); err != nil {
		return nil, err
	}
	if err := validatePort("register-public", pubPort); err != nil {
		return nil, err
	}

	return &tunnelBackend{
		id:          net.JoinHostPort(host, ctrlPort),
		host:        host,
		controlPort: ctrlPort,
		publicPort:  pubPort,
		namespace:   normalizeNamespace(req.Namespace),
		dynamic:     true,
		fleetID:     strings.TrimSpace(req.FleetID),
		lastSeen:    time.Now(),
	}, nil
}

func (m *LBManager) registerDynamicBackend(req lbRegisterRequest) (*tunnelBackend, error) {
	if !*lbDynamic {
		return nil, fmt.Errorf("dynamic registration is disabled on this LB")
	}

	backend, err := backendFromRegisterRequest(req)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for i, existing := range m.backends {
		if existing.id == backend.id {
			m.backends[i].host = backend.host
			m.backends[i].controlPort = backend.controlPort
			m.backends[i].publicPort = backend.publicPort
			m.backends[i].namespace = backend.namespace
			m.backends[i].dynamic = true
			m.backends[i].fleetID = backend.fleetID
			m.backends[i].lastSeen = time.Now()
			return m.backends[i], nil
		}
	}

	m.backends = append(m.backends, backend)
	if _, ok := m.load[backend.id]; !ok {
		m.load[backend.id] = 0
	}
	return backend, nil
}

func (m *LBManager) deregisterDynamicBackend(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, backend := range m.backends {
		if backend.id != id || !backend.dynamic {
			continue
		}
		m.backends = append(m.backends[:i], m.backends[i+1:]...)
		delete(m.load, id)
		for key, route := range m.routes {
			if route.id == id {
				delete(m.routes, key)
			}
		}
		return true
	}
	return false
}

func (m *LBManager) touchDynamicBackend(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, backend := range m.backends {
		if backend.id == id && backend.dynamic {
			backend.lastSeen = time.Now()
			return true
		}
	}
	return false
}

func (m *LBManager) eligibleBackends(backends []*tunnelBackend) []*tunnelBackend {
	if len(backends) == 0 {
		return backends
	}

	cutoff := time.Now().Add(-time.Duration(*lbRegisterTTL) * time.Second)
	eligible := make([]*tunnelBackend, 0, len(backends))
	for _, b := range backends {
		if !b.dynamic || b.lastSeen.After(cutoff) {
			eligible = append(eligible, b)
		}
	}
	return eligible
}

func (m *LBManager) serveDynamicPrune(ctx context.Context) {
	if !*lbDynamic {
		return
	}
	ticker := time.NewTicker(time.Duration(*lbRegisterTTL/2) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pruneStaleBackends()
		}
	}
}

func (m *LBManager) pruneStaleBackends() {
	cutoff := time.Now().Add(-time.Duration(*lbRegisterTTL) * time.Second)

	m.mu.Lock()
	defer m.mu.Unlock()

	alive := m.backends[:0]
	for _, backend := range m.backends {
		if !backend.dynamic || backend.lastSeen.After(cutoff) {
			alive = append(alive, backend)
			continue
		}
		if !*quiet {
			log.Printf("LB pruned stale barge backend %s (fleet %s)", backend.id, backend.fleetID)
		}
		delete(m.load, backend.id)
		for key, route := range m.routes {
			if route.id == backend.id {
				delete(m.routes, key)
			}
		}
	}
	m.backends = alive
}

func writeLBRegisterResponse(w http.ResponseWriter, status int, resp lbRegisterResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}