package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
	"golang.org/x/crypto/acme/autocert"
)

func (m *ServerManager) closeAllTunnels() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, t := range m.tunnels {
		if t != nil && t.Session != nil {
			_ = t.Session.Close()
		}
		delete(m.tunnels, key)
	}
}

func runServer() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	if err := loadVHosts(); err != nil {
		log.Fatalf("vhosts config: %v", err)
	}

	if meshActive() {
		auth, err := startMeshAuthority()
		if err != nil {
			log.Fatalf("mesh authority: %v", err)
		}
		if auth != nil {
			defer auth.Close()
		}
	}

	stopAnycast := startAnycastSideCar(ctx)
	defer stopAnycast()

	manager := &ServerManager{
		tunnels: make(map[string]*liveTunnel),
		pending: make(map[string]SnapshotTunnel),
	}
	manager.maybeRestoreOnStart()

	certs := buildCertProvider()
	if certs.acmeMgr != nil && *acmeHTTP {
		go serveACME(ctx, certs.acmeMgr)
	}

	controlAddr := ":" + *controlPort
	// Dual ALPN: "tunneltug" (yamux barge clients) + "secure-overlay" (gonode / secure_network mesh).
	controlLn, err := listenControlQUICDual(controlAddr, certs.controlTLS)
	if err != nil {
		log.Fatalf("Failed to bind QUIC control port: %v", err)
	}
	var meshAgent *meshAgentGateway
	if meshActive() {
		var merr error
		meshAgent, merr = newMeshAgentGateway()
		if merr != nil {
			log.Printf("[mesh-agent] gateway keys: %v (secure-overlay agents disabled)", merr)
		} else {
			log.Printf("[mesh-agent] secure-overlay gateway ready on QUIC %s pubkey=%s", controlAddr, meshAgent.PubHex())
			log.Printf("[mesh-agent] gonode: -gateway 127.0.0.1:%s -gateway-pub %s", strings.TrimPrefix(controlAddr, ":"), meshAgent.PubHex())
		}
	}
	log.Printf("Listening for tunnel clients on QUIC %s (TLS ALPN tunneltug|secure-overlay)", controlAddr)
	go manager.serveControl(ctx, controlLn, meshAgent)

	ingress := manager.startPublicHTTP(certs)
	startServerLBRegistration(ctx)
	go manager.runPeriodicSnapshots(ctx)

	<-ctx.Done()
	// Snapshot while tunnels are still recorded, then tear down.
	manager.maybeSnapshotOnShutdown()
	manager.closeAllTunnels()
	gracefulShutdown("server", &serverRuntime{control: controlLn, ingress: ingress})
}

func serveACME(ctx context.Context, mgr *autocert.Manager) {
	server := &http.Server{Addr: ":80", Handler: mgr.HTTPHandler(nil)}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Println("Listening for ACME HTTP-01 challenges on :80")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("ACME HTTP challenge listener stopped: %v", err)
	}
}

func (m *ServerManager) serveControl(ctx context.Context, listener *quic.Listener, meshAgent *meshAgentGateway) {
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go func(c *quic.Conn) {
			proto := ""
			if st := c.ConnectionState(); st.TLS.NegotiatedProtocol != "" {
				proto = st.TLS.NegotiatedProtocol
			}
			// 0TrustOS / secure_network mesh agents (Noise), not yamux barge clients.
			if proto == meshAgentALPN && meshAgent != nil {
				meshAgent.handleConn(ctx, c)
				return
			}
			controlConn, err := acceptControlQUICConn(ctx, c)
			if err != nil {
				return
			}
			m.handleClientConnection(controlConn)
		}(conn)
	}
}

func (m *ServerManager) handleClientConnection(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in client handler: %v", r)
		}
	}()

	var msg ControlMessage
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&msg); err != nil {
		log.Printf("Invalid control message from %s", conn.RemoteAddr())
		conn.Close()
		return
	}

	if !tokensEqual(msg.Token, *authToken) {
		log.Printf("Unauthorized client attempt from %s", conn.RemoteAddr())
		conn.Close()
		return
	}

	session, err := yamux.Client(conn, streamingYamuxConfig())
	if err != nil {
		log.Printf("Failed to create yamux session: %v", err)
		conn.Close()
		return
	}

	tunnelKey := composeTunnelKey(msg.Namespace, msg.Subdomain)
	ns := normalizeNamespace(msg.Namespace)
	sub := msg.Subdomain
	if isDirectRouting() {
		sub = defaultTunnelKey
	}

	rec := &liveTunnel{
		Namespace:   ns,
		Subdomain:   sub,
		Remote:      conn.RemoteAddr().String(),
		ConnectedAt: time.Now().UTC(),
		Session:     session,
	}

	m.mu.Lock()
	if existing, ok := m.tunnels[tunnelKey]; ok && existing != nil && existing.Session != nil {
		existing.Session.Close()
	}
	m.tunnels[tunnelKey] = rec
	delete(m.pending, tunnelKey)
	m.mu.Unlock()

	log.Printf("Tunnel established: %s -> %s", tunnelKey, conn.RemoteAddr())
	publishTunnelToMesh(msg.Namespace, msg.Subdomain)

	<-session.CloseChan()
	m.mu.Lock()
	if cur, ok := m.tunnels[tunnelKey]; ok && cur == rec {
		delete(m.tunnels, tunnelKey)
	}
	m.mu.Unlock()
	unpublishTunnelFromMesh(msg.Namespace, msg.Subdomain)
	log.Printf("Tunnel disconnected: %s", tunnelKey)
}

func (m *ServerManager) startPublicHTTP(certs *certProvider) *publicIngress {
	proxy := streamingReverseProxy(m)
	handler := m.publicHandler(proxy)

	routingMode := "subdomain"
	if isDirectRouting() {
		routingMode = "direct"
	}

	addr := ":" + *publicPort
	ingress := &publicIngress{
		http: productionHTTPServer(addr, handler, certs.publicTLS),
	}

	if certs.publicTLS != nil && *http3Enabled {
		ingress.http3 = productionHTTP3Server(addr, handler, certs.publicTLS, publicPortNumber())
		wrapped := withQUICAltSvc(ingress.http3, handler)
		ingress.http.Handler = wrapped
		ingress.http3.Handler = wrapped

		log.Printf("Listening for Public HTTP/3 (QUIC) on %s (routing: %s, buffer: %dKB)", addr, routingMode, streamBufferSize()/1024)
		go func() {
			if err := ingress.http3.ListenAndServe(); err != nil {
				log.Fatalf("HTTP/3 server died: %v", err)
			}
		}()
	}

	if certs.publicTLS != nil {
		log.Printf("Listening for Public HTTPS (streaming) on %s (routing: %s, buffer: %dKB)", addr, routingMode, streamBufferSize()/1024)
		go func() {
			if err := ingress.http.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTPS server died: %v", err)
			}
		}()
	} else {
		log.Printf("Listening for Public HTTP (streaming) on %s (routing: %s, buffer: %dKB)", addr, routingMode, streamBufferSize()/1024)
		go func() {
			if err := ingress.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP server died: %v", err)
			}
		}()
	}

	return ingress
}
