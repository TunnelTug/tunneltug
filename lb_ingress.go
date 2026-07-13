package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func (m *LBManager) startLBPublicHTTP(certs *certProvider) *publicIngress {
	handler := m.lbPublicHandler()

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

		log.Printf("LB listening for Public HTTP/3 (QUIC) on %s (routing: %s, policy: %s)", addr, routingMode, *lbPolicy)
		go func() {
			if err := ingress.http3.ListenAndServe(); err != nil {
				log.Fatalf("LB HTTP/3 server died: %v", err)
			}
		}()
	}

	if certs.publicTLS != nil {
		log.Printf("LB listening for Public HTTPS on %s (routing: %s, policy: %s)", addr, routingMode, *lbPolicy)
		go func() {
			if err := ingress.http.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("LB HTTPS server died: %v", err)
			}
		}()
	} else {
		log.Printf("LB listening for Public HTTP on %s (routing: %s, policy: %s)", addr, routingMode, *lbPolicy)
		go func() {
			if err := ingress.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("LB HTTP server died: %v", err)
			}
		}()
	}

	return ingress
}

func (m *LBManager) lbPublicHandler() http.Handler {
	mux := http.NewServeMux()

	m.mountRegisterHandlers(mux)
	mountMeshHandlers(mux)
	mountVHostAdminHandlers(mux)

	mux.HandleFunc("/_tunneltug/health", func(w http.ResponseWriter, r *http.Request) {
		m.mu.RLock()
		backendCount := len(m.backends)
		dynamicCount := 0
		for _, b := range m.backends {
			if b.dynamic {
				dynamicCount++
			}
		}
		routeCount := len(m.routes)
		m.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","mode":"lb","routing":"%s","policy":"%s","backends":%d,"dynamic_backends":%d,"dynamic_registration":%t,"routes":%d,"http3":%t,"mesh":%t,"vhosts":%d,"active_streams":%d,"total_streams":%d}`,
			*routing, *lbPolicy, backendCount, dynamicCount, *lbDynamic, routeCount, *http3Enabled, meshAuthorityActive(), vhostCount(), activeStreams.Load(), totalStreams.Load())
	})

	mux.HandleFunc("/", m.handleLBTraffic)

	return mux
}

func (m *LBManager) backendReverseProxy(backend *tunnelBackend) *httputil.ReverseProxy {
	target := &url.URL{
		Scheme: backend.publicScheme(),
		Host:   backend.publicAddr(),
	}

	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			originalHost := req.Host
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = originalHost

			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				prior := req.Header.Get("X-Forwarded-For")
				if prior == "" {
					req.Header.Set("X-Forwarded-For", clientIP)
				} else {
					req.Header.Set("X-Forwarded-For", prior+", "+clientIP)
				}
			}
			req.Header.Set("X-Forwarded-Proto", publicScheme())
			req.Header.Set("X-Forwarded-Host", originalHost)
		},
		Transport:     lbBackendTransport(backend),
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !*quiet {
				log.Printf("LB proxy error for %s %s via %s: %v", r.Method, r.URL.Path, backend.id, err)
			}
			http.Error(w, "Tunnel backend unavailable", http.StatusBadGateway)
		},
	}
}

func lbBackendTransport(backend *tunnelBackend) *http.Transport {
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		MaxIdleConns:          256,
		IdleConnTimeout:       0,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 0,
	}

	if backend.publicScheme() == "https" {
		transport.TLSClientConfig = backendTLSConfig()
	}

	return transport
}

func (m *LBManager) handleLBTraffic(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	host = normalizeHost(host)

	// Product apex before tunnel LB routing (same as server mode).
	if h := matchProductVHost(host); h != nil {
		h.ServeHTTP(w, r)
		return
	}

	key := tunnelKeyFromHost(host)
	if key == "" {
		m.writeLBRouteHint(w)
		return
	}

	backend, err := m.routeBackend(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if isHTTP3WebSocketUpgrade(r) {
		upstream, err := dialBackendUpgradeConn(backend)
		if err != nil {
			http.Error(w, "Tunnel backend unavailable", http.StatusBadGateway)
			return
		}
		proxyHTTP3UpgradeOverConn(w, r, upstream)
		return
	}

	proxy := m.backendReverseProxy(backend)
	proxy.ServeHTTP(w, r)
}

func (m *LBManager) writeLBRouteHint(w http.ResponseWriter) {
	if isDirectRouting() {
		http.Error(w, "Direct tunnel not connected", http.StatusNotFound)
		return
	}
	http.Error(w, fmt.Sprintf("Use a host like %s://%s:%s (LB subdomain routing)", publicScheme(), namespaceRouteHint(), *publicPort), http.StatusNotFound)
}