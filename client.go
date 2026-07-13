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

		log.Printf("Connected! Tunnel online. Public URL: %s", publicURL())

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

		for ctx.Err() == nil {
			stream, err := session.Accept()
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				log.Println("Server disconnected. Reconnecting...")
				break
			}
			go proxyStreamToLocal(stream, localApp)
		}

		active.Lock()
		active.conn = nil
		active.session = nil
		active.Unlock()
		_ = session.Close()
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
