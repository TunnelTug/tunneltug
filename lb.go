package main

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type tunnelBackend struct {
	id          string
	host        string
	controlPort string
	publicPort  string
	namespace   string
	dynamic     bool
	fleetID     string
	lastSeen    time.Time
}

func (b *tunnelBackend) controlAddr() string {
	return net.JoinHostPort(b.host, b.controlPort)
}

func (b *tunnelBackend) publicAddr() string {
	return net.JoinHostPort(b.host, b.publicPort)
}

func (b *tunnelBackend) publicScheme() string {
	if *prod || *dev {
		return "https"
	}
	return "http"
}

type LBManager struct {
	mu        sync.RWMutex
	backends  []*tunnelBackend
	routes    map[string]*tunnelBackend
	load      map[string]int
	rrCounter uint64
}

func newLBManager(backends []*tunnelBackend) *LBManager {
	load := make(map[string]int, len(backends))
	for _, b := range backends {
		load[b.id] = 0
	}
	return &LBManager{
		backends: backends,
		routes:   make(map[string]*tunnelBackend),
		load:     load,
	}
}

func parseStaticBackends(spec string) ([]*tunnelBackend, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return []*tunnelBackend{}, nil
	}
	return parseBackends(spec)
}

func parseBackends(spec string) ([]*tunnelBackend, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("at least one backend is required (use -backends)")
	}

	parts := strings.Split(spec, ",")
	backends := make([]*tunnelBackend, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		fields := strings.Split(part, ":")
		if len(fields) < 1 || len(fields) > 4 {
			return nil, fmt.Errorf("invalid backend %q: use host, host:control, host:control:public, or host:control:public:namespace", part)
		}

		host := strings.TrimSpace(fields[0])
		if host == "" {
			return nil, fmt.Errorf("invalid backend %q: host is required", part)
		}

		controlPort := strings.TrimSpace(*controlPort)
		publicPort := strings.TrimSpace(*publicPort)
		if len(fields) >= 2 && strings.TrimSpace(fields[1]) != "" {
			controlPort = strings.TrimSpace(fields[1])
		}
		if len(fields) == 3 && strings.TrimSpace(fields[2]) != "" {
			publicPort = strings.TrimSpace(fields[2])
		}

		if err := validatePort("backend-control", controlPort); err != nil {
			return nil, fmt.Errorf("backend %q: %w", part, err)
		}
		if err := validatePort("backend-public", publicPort); err != nil {
			return nil, fmt.Errorf("backend %q: %w", part, err)
		}

		ns := defaultNamespace
		if len(fields) == 4 && strings.TrimSpace(fields[3]) != "" {
			ns = normalizeNamespace(fields[3])
		}

		id := net.JoinHostPort(host, controlPort)
		backends = append(backends, &tunnelBackend{
			id:          id,
			host:        host,
			controlPort: controlPort,
			publicPort:  publicPort,
			namespace:   ns,
		})
	}

	if len(backends) == 0 {
		return nil, fmt.Errorf("at least one backend is required (use -backends)")
	}

	return backends, nil
}

func runLB() {
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

	if *lbDynamic {
		log.Printf("LB dynamic barge registration enabled (ttl %ds)", *lbRegisterTTL)
	}
	log.Printf("LB listening for tunnel clients on QUIC %s (%d static backends)", controlAddr, len(backends))
	for _, b := range backends {
		log.Printf("  backend %s (control %s, public %s)", b.id, b.controlAddr(), b.publicAddr())
	}

	go manager.serveLBControl(ctx, controlLn)
	go manager.serveDynamicPrune(ctx)

	ingress := manager.startLBPublicHTTP(certs)

	<-ctx.Done()
	gracefulShutdown("lb", &serverRuntime{control: controlLn, ingress: ingress})
}

func (m *LBManager) backendByID(id string) *tunnelBackend {
	for _, b := range m.backends {
		if b.id == id && m.backendAvailable(b) {
			return b
		}
	}
	return nil
}

func (m *LBManager) backendAvailable(b *tunnelBackend) bool {
	if b == nil {
		return false
	}
	if !b.dynamic {
		return true
	}
	cutoff := time.Now().Add(-time.Duration(*lbRegisterTTL) * time.Second)
	return b.lastSeen.After(cutoff)
}