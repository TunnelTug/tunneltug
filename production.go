package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

var (
	activeStreams atomic.Int64
	totalStreams  atomic.Int64
)

func productionHTTPServer(addr string, handler http.Handler, tlsConfig *tls.Config) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       0,
		MaxHeaderBytes:    1 << 20,
	}
}

func streamingReverseProxy(m *ServerManager) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = req.Host

			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				prior := req.Header.Get("X-Forwarded-For")
				if prior == "" {
					req.Header.Set("X-Forwarded-For", clientIP)
				} else {
					req.Header.Set("X-Forwarded-For", prior+", "+clientIP)
				}
			}
			req.Header.Set("X-Forwarded-Proto", publicScheme())
			req.Header.Set("X-Forwarded-Host", req.Host)
		},
		Transport:     streamingProxyTransport(m),
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !*quiet {
				log.Printf("Proxy error for %s %s: %v", r.Method, r.URL.Path, err)
			}
			http.Error(w, "Tunnel upstream unavailable", http.StatusBadGateway)
		},
	}
}

func streamingProxyTransport(m *ServerManager) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           m.tunnelDialContext,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxIdleConns:          0,
		IdleConnTimeout:       0,
		TLSHandshakeTimeout:   0,
		ExpectContinueTimeout: 0,
		ResponseHeaderTimeout: 0,
	}
}

func (m *ServerManager) tunnelDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	key := tunnelKeyFromHost(host)
	session, err := m.tunnelSession(key)
	if err != nil {
		return nil, err
	}
	return openTunnelStream(session)
}

func (m *ServerManager) tunnelSession(key string) (*yamux.Session, error) {
	m.mu.RLock()
	rec, ok := m.tunnels[key]
	m.mu.RUnlock()
	if !ok || rec == nil || rec.Session == nil {
		if isDirectRouting() {
			return nil, fmt.Errorf("direct tunnel not connected")
		}
		return nil, fmt.Errorf("tunnel for subdomain '%s' not found", key)
	}
	return rec.Session, nil
}

func (m *ServerManager) publicHandler(proxy *httputil.ReverseProxy) http.Handler {
	mux := http.NewServeMux()

	mountMeshHandlers(mux)
	m.mountSnapshotHandlers(mux)
	// Hot vhost upstream cutover (same public domain → primary or standby k3s pod)
	mountVHostAdminHandlers(mux)

	mux.HandleFunc("/_tunneltug/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		meshOn := meshAuthorityActive()
		m.mu.RLock()
		live := len(m.tunnels)
		pending := len(m.pending)
		m.mu.RUnlock()
		fmt.Fprintf(w, `{"status":"ok","routing":"%s","namespace":"%s","http3":%t,"http3_websocket":%t,"mesh":%t,"vhosts":%d,"active_streams":%d,"total_streams":%d,"live_tunnels":%d,"pending_tunnels":%d,"snapshot":%t}`,
			*routing, normalizeNamespace(*namespace), *http3Enabled, *http3Enabled, meshOn, vhostCount(), activeStreams.Load(), totalStreams.Load(), live, pending, snapshotActive())
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = normalizeHost(host)

		// Product apex/www (and optional single-label wildcards) before tunnel routing.
		if h := matchProductVHost(host); h != nil {
			h.ServeHTTP(w, r)
			return
		}

		key := tunnelKeyFromHost(host)
		if key == "" {
			m.writeRouteHint(w)
			return
		}

		session, err := m.tunnelSession(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		if isHTTP3WebSocketUpgrade(r) {
			proxyHTTP3Upgrade(w, r, session)
			return
		}
		if isStreamingUpgrade(r) {
			proxyUpgrade(w, r, session)
			return
		}

		proxy.ServeHTTP(w, r)
	})

	return mux
}

func (m *ServerManager) writeRouteHint(w http.ResponseWriter) {
	if isDirectRouting() {
		http.Error(w, "Direct tunnel not connected", http.StatusNotFound)
		return
	}
	http.Error(w, fmt.Sprintf("Use a host like %s://%s:%s (subdomain routing mode)", publicScheme(), namespaceRouteHint(), *publicPort), http.StatusNotFound)
}

func clientBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 2 * time.Second
	}
	delay := time.Duration(1<<minInt(attempt, 6)) * time.Second
	if delay > 60*time.Second {
		return 60 * time.Second
	}
	return delay
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
