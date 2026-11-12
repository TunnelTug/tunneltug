package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

func runClient() {
	serverCtrl := fmt.Sprintf("%s:%s", *serverIP, *controlPort)
	localApp := fmt.Sprintf("127.0.0.1:%s", *localPort)

	ctx, stop := notifyShutdownContext()
	defer stop()

	startVPIStub(ctx)
	startMeshRuntime(ctx)

	if !*quiet {
		go runLocalDashboard(ctx, serverCtrl, localApp)
	}

	var active struct {
		sync.Mutex
		conn    net.Conn
		session *yamux.Session
	}
	go func() {
		<-ctx.Done()
		active.Lock()
		if active.session != nil {
			_ = active.session.Close()
		}
		if active.conn != nil {
			_ = active.conn.Close()
		}
		active.Unlock()
	}()

	attempt := 0

	for ctx.Err() == nil {
		log.Printf("Dialing server at %s via QUIC...", serverCtrl)

		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		conn, err := dialControlQUIC(dialCtx, serverCtrl)
		cancel()

		if err != nil {
			attempt++
			delay := clientBackoff(attempt)
			log.Printf("Connection failed: %v. Retrying in %s...", err, delay)
			if !sleepOrDone(ctx, delay) {
				break
			}
			continue
		}
		attempt = 0

		msg := ControlMessage{
			Token:     *authToken,
			Namespace: normalizeNamespace(*namespace),
			Subdomain: *subdomain,
		}
		if err := json.NewEncoder(conn).Encode(msg); err != nil {
			log.Printf("Failed to send auth: %v", err)
			conn.Close()
			continue
		}

		session, err := yamux.Server(conn, streamingYamuxConfig())
		if err != nil {
			log.Printf("Yamux error: %v", err)
			conn.Close()
			continue
		}

		active.Lock()
		active.conn = conn
		active.session = session
		active.Unlock()

		onlineAt := time.Now()
		log.Printf("Control channel open. Public URL: %s", publicURL())

		for ctx.Err() == nil {
			stream, err := session.Accept()
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				attempt++
				delay := clientBackoff(attempt)
				log.Printf("%s Retrying in %s...", tunnelCloseHint(err, time.Since(onlineAt)), delay)
				_ = session.Close()
				active.Lock()
				active.conn = nil
				active.session = nil
				active.Unlock()
				if !sleepOrDone(ctx, delay) {
					return
				}
				break
			}
			attempt = 0
			go proxyStreamToLocal(stream, localApp)
		}

		active.Lock()
		active.conn = nil
		active.session = nil
		active.Unlock()
		_ = session.Close()
	}
}

func tunnelCloseHint(err error, alive time.Duration) string {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "unauthorized") || strings.Contains(low, "application error 0x1"):
		return "Server rejected tunnel auth (wrong -token for this control port)."
	case strings.Contains(low, "invalid control") || strings.Contains(low, "application error 0x2"):
		return "Server rejected the control handshake."
	case alive < 2*time.Second:
		return fmt.Sprintf("Server closed immediately (%v). Wrong token, or another client took this tunnel key.", err)
	default:
		return "Server disconnected."
	}
}

func proxyStreamToLocal(stream net.Conn, localAddr string) {
	defer stream.Close()
	tuneTCPConn(stream)

	if *maxStreams > 0 && activeStreams.Load() >= int64(*maxStreams) {
		_, _ = stream.Write([]byte("HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\n\r\nToo many concurrent streams."))
		return
	}

	activeStreams.Add(1)
	totalStreams.Add(1)
	defer activeStreams.Add(-1)

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	local, err := dialer.Dial("tcp", localAddr)
	if err != nil {
		_, _ = stream.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\nLocal application is not running."))
		return
	}
	tuneTCPConn(local)
	relayConns(stream, local)
}

func runLocalDashboard(ctx context.Context, server string, local string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		tunnelLabel := *subdomain
		if isDirectRouting() {
			tunnelLabel = "(direct — no subdomain)"
		}
		meshLine := ""
		if meshActive() {
			meshLine = fmt.Sprintf("<li>Mesh: %s (VPI stub %s)</li>", meshPrivateName(meshHostID()), strings.TrimSpace(*vpiListen))
		}
		fmt.Fprintf(w, `<html><body><h2>TunnelTug Client</h2><ul><li>Status: Online</li><li>Routing: %s</li><li>Protocol: %s</li><li>Server: %s</li><li>Target: %s</li><li>Tunnel: %s</li><li>Public URL: %s</li>%s<li>Active Streams: %d</li><li>Total Streams: %d</li><li>Buffer: %dKB</li></ul></body></html>`,
			*routing, *protocol, server, local, tunnelLabel, publicURL(), meshLine,
			activeStreams.Load(), totalStreams.Load(), streamBufferSize()/1024)
	})
	log.Printf("Client dashboard at http://127.0.0.1:%s", *dashPort)
	dash := &http.Server{
		Addr:              "127.0.0.1:" + *dashPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = dash.Shutdown(shutdownCtx)
	}()
	if err := dash.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("Dashboard stopped: %v", err)
	}
}
