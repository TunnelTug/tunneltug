package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

func (m *LBManager) serveLBControl(ctx context.Context, listener *quic.Listener) {
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go func(c *quic.Conn) {
			controlConn, err := acceptControlQUICConn(ctx, c)
			if err != nil {
				return
			}
			m.handleLBClientConnection(ctx, controlConn)
		}(conn)
	}
}

func (m *LBManager) handleLBClientConnection(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()

	var msg ControlMessage
	var jsonBuf bytes.Buffer
	tee := io.TeeReader(clientConn, &jsonBuf)
	if err := json.NewDecoder(tee).Decode(&msg); err != nil {
		log.Printf("Invalid control message from %s", clientConn.RemoteAddr())
		return
	}

	if !tokensEqual(msg.Token, *authToken) {
		log.Printf("Unauthorized client attempt from %s", clientConn.RemoteAddr())
		return
	}

	tunnelKey := composeTunnelKey(msg.Namespace, msg.Subdomain)

	backend, err := m.pickBackend(tunnelKey)
	if err != nil {
		log.Printf("No backend available for %s: %v", tunnelKey, err)
		return
	}

	m.registerRoute(tunnelKey, backend)

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	backendConn, err := dialBackendControlQUIC(dialCtx, backend.controlAddr())
	cancel()
	if err != nil {
		log.Printf("Failed to dial backend %s for %s: %v", backend.id, tunnelKey, err)
		m.unregisterRoute(tunnelKey, backend)
		return
	}
	defer backendConn.Close()

	if _, err := io.Copy(backendConn, &jsonBuf); err != nil {
		log.Printf("Failed to forward auth to backend %s: %v", backend.id, err)
		m.unregisterRoute(tunnelKey, backend)
		return
	}

	m.incLoad(backend.id)
	defer m.decLoad(backend.id)

	log.Printf("Tunnel relayed: %s -> backend %s (%s)", tunnelKey, backend.id, clientConn.RemoteAddr())
	relayConns(clientConn, backendConn)
	log.Printf("Tunnel relay closed: %s (backend %s)", tunnelKey, backend.id)
}

func (m *LBManager) pickBackend(tunnelKey string) (*tunnelBackend, error) {
	m.mu.RLock()
	if existing, ok := m.routes[tunnelKey]; ok && m.backendByID(existing.id) != nil {
		m.mu.RUnlock()
		return existing, nil
	}
	allBackends := m.eligibleBackends(append([]*tunnelBackend(nil), m.backends...))
	m.mu.RUnlock()

	ns, _ := splitTunnelKey(tunnelKey)
	backends := make([]*tunnelBackend, 0, len(allBackends))
	for _, b := range allBackends {
		if normalizeNamespace(b.namespace) == ns {
			backends = append(backends, b)
		}
	}

	if len(backends) == 0 {
		return nil, fmt.Errorf("no backends available for namespace %q", ns)
	}

	switch parseLBPolicy(*lbPolicy) {
	case assignRoundRobin:
		return m.pickRoundRobin(backends), nil
	default:
		return m.pickLeastLoaded(backends), nil
	}
}

func (m *LBManager) pickLeastLoaded(backends []*tunnelBackend) *tunnelBackend {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var chosen *tunnelBackend
	minLoad := -1
	for _, b := range backends {
		load := m.load[b.id]
		if chosen == nil || load < minLoad {
			chosen = b
			minLoad = load
		}
	}
	return chosen
}

func (m *LBManager) pickRoundRobin(backends []*tunnelBackend) *tunnelBackend {
	idx := atomic.AddUint64(&m.rrCounter, 1) - 1
	return backends[int(idx)%len(backends)]
}

func (m *LBManager) registerRoute(tunnelKey string, backend *tunnelBackend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[tunnelKey] = backend
}

func (m *LBManager) unregisterRoute(tunnelKey string, backend *tunnelBackend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.routes[tunnelKey]; ok && current.id == backend.id {
		delete(m.routes, tunnelKey)
	}
}

func (m *LBManager) incLoad(backendID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.load[backendID]++
}

func (m *LBManager) decLoad(backendID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.load[backendID] > 0 {
		m.load[backendID]--
	}
}

func (m *LBManager) routeBackend(tunnelKey string) (*tunnelBackend, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	backend, ok := m.routes[tunnelKey]
	if !ok || m.backendByID(backend.id) == nil {
		if isDirectRouting() {
			if len(m.backends) == 1 {
				return m.backends[0], nil
			}
		}
		return nil, fmt.Errorf("no backend route for subdomain %q", tunnelKey)
	}
	return backend, nil
}

type lbAssignPolicy string

const (
	assignSticky     lbAssignPolicy = "sticky"
	assignRoundRobin lbAssignPolicy = "round-robin"
)

func parseLBPolicy(value string) lbAssignPolicy {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "round-robin", "rr":
		return assignRoundRobin
	default:
		return assignSticky
	}
}