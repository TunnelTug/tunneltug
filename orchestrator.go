package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"
)

func runOrchestrator() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	if meshActive() {
		auth, err := startMeshAuthority()
		if err != nil {
			log.Fatalf("mesh authority: %v", err)
		}
		if auth != nil {
			defer auth.Close()
		}
	}

	backends, err := parseStaticBackends(*lbBackends)
	if err != nil {
		log.Fatalf("Backend configuration error: %v", err)
	}
	if len(backends) == 0 && !*lbDynamic {
		log.Fatalf("Backend configuration error: at least one backend is required (use -backends or -lb-dynamic=true)")
	}

	manager := newLBManager(backends)

	certs := buildCertProvider()
	if certs.acmeMgr != nil && *acmeHTTP {
		go serveACME(ctx, certs.acmeMgr)
	}

	controlAddr := ":" + *controlPort
	controlLn, err := listenControlQUIC(controlAddr, certs.controlTLS)
	if err != nil {
		log.Fatalf("Failed to bind QUIC control port: %v", err)
	}

	log.Printf("Orchestrator listening on QUIC %s (namespace routing enabled)", controlAddr)
	if *lbDynamic {
		log.Printf("Dynamic barge registration enabled (ttl %ds)", *lbRegisterTTL)
	}
	for _, b := range backends {
		log.Printf("  backend %s namespace=%s (control %s, public %s)", b.id, b.namespace, b.controlAddr(), b.publicAddr())
	}

	go manager.serveLBControl(ctx, controlLn)
	go manager.serveDynamicPrune(ctx)

	ingress := manager.startOrchestratorHTTP(certs)
	if !*quiet {
		go runOrchestratorDashboard(ctx, manager)
	}

	<-ctx.Done()
	gracefulShutdown("orchestrator", &serverRuntime{control: controlLn, ingress: ingress})
}

func (m *LBManager) startOrchestratorHTTP(certs *certProvider) *publicIngress {
	handler := m.orchestratorPublicHandler()
	addr := ":" + *publicPort
	ingress := &publicIngress{
		http: productionHTTPServer(addr, handler, certs.publicTLS),
	}

	if certs.publicTLS != nil && *http3Enabled {
		ingress.http3 = productionHTTP3Server(addr, handler, certs.publicTLS, publicPortNumber())
		wrapped := withQUICAltSvc(ingress.http3, handler)
		ingress.http.Handler = wrapped
		ingress.http3.Handler = wrapped
		log.Printf("Orchestrator listening for Public HTTP/3 (QUIC) on %s", addr)
		go func() {
			if err := ingress.http3.ListenAndServe(); err != nil {
				log.Fatalf("Orchestrator HTTP/3 server died: %v", err)
			}
		}()
	}

	if certs.publicTLS != nil {
		log.Printf("Orchestrator listening for Public HTTPS on %s", addr)
		go func() {
			if err := ingress.http.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Orchestrator HTTPS server died: %v", err)
			}
		}()
	} else {
		log.Printf("Orchestrator listening for Public HTTP on %s", addr)
		go func() {
			if err := ingress.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Orchestrator HTTP server died: %v", err)
			}
		}()
	}

	return ingress
}

func (m *LBManager) orchestratorPublicHandler() http.Handler {
	mux := http.NewServeMux()
	m.mountRegisterHandlers(mux)
	mountMeshHandlers(mux)

	mux.HandleFunc("/_tunneltug/health", func(w http.ResponseWriter, r *http.Request) {
		summary := m.namespaceSummary()
		m.mu.RLock()
		backendCount := len(m.backends)
		routeCount := len(m.routes)
		m.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","mode":"orchestrator","routing":"%s","policy":"%s","namespaces":%d,"backends":%d,"dynamic_backends":%d,"dynamic_registration":%t,"routes":%d,"http3":%t,"http3_websocket":%t,"mesh":%t,"active_streams":%d,"total_streams":%d}`,
			*routing, *lbPolicy, len(summary), backendCount, m.dynamicBackendCount(), *lbDynamic, routeCount, *http3Enabled, *http3Enabled, meshAuthorityActive(), activeStreams.Load(), totalStreams.Load())
	})

	mux.HandleFunc("/_tunneltug/orchestrator/namespaces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"namespaces": m.namespaceSummary(),
		})
	})

	mux.HandleFunc("/", m.handleLBTraffic)
	return mux
}

func (m *LBManager) dynamicBackendCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, b := range m.backends {
		if b.dynamic {
			count++
		}
	}
	return count
}

func (m *LBManager) namespaceSummary() []map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type agg struct {
		backends int
		routes   int
		load     int
	}
	byNS := make(map[string]*agg)
	for _, b := range m.backends {
		ns := normalizeNamespace(b.namespace)
		if byNS[ns] == nil {
			byNS[ns] = &agg{}
		}
		byNS[ns].backends++
		byNS[ns].load += m.load[b.id]
	}
	for key := range m.routes {
		ns, _ := splitTunnelKey(key)
		if byNS[ns] == nil {
			byNS[ns] = &agg{}
		}
		byNS[ns].routes++
	}

	names := make([]string, 0, len(byNS))
	for ns := range byNS {
		names = append(names, ns)
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(names))
	for _, ns := range names {
		item := byNS[ns]
		out = append(out, map[string]any{
			"name":     ns,
			"backends": item.backends,
			"routes":   item.routes,
			"load":     item.load,
		})
	}
	return out
}

func (m *LBManager) backendsForNamespace(namespace string) []*tunnelBackend {
	ns := normalizeNamespace(namespace)
	all := m.eligibleBackends(append([]*tunnelBackend(nil), m.backends...))
	filtered := make([]*tunnelBackend, 0, len(all))
	for _, b := range all {
		if normalizeNamespace(b.namespace) == ns {
			filtered = append(filtered, b)
		}
	}
	return filtered
}

func runOrchestratorDashboard(ctx context.Context, manager *LBManager) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h2>TunnelTug Orchestrator</h2><p><a href="/_tunneltug/orchestrator/namespaces">Namespaces JSON</a> | <a href="/_tunneltug/health">Health</a></p><table border="1" cellpadding="6"><tr><th>Namespace</th><th>Backends</th><th>Routes</th><th>Load</th></tr>`)
		for _, entry := range manager.namespaceSummary() {
			fmt.Fprintf(w, `<tr><td>%v</td><td>%v</td><td>%v</td><td>%v</td></tr>`,
				entry["name"], entry["backends"], entry["routes"], entry["load"])
		}
		fmt.Fprint(w, `</table></body></html>`)
	})

	addr := "127.0.0.1:" + *orchDashPort
	log.Printf("Orchestrator dashboard at http://%s", addr)
	dash := &http.Server{
		Addr:              addr,
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
		log.Printf("Orchestrator dashboard stopped: %v", err)
	}
}