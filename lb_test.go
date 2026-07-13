package main

import (
	"strings"
	"testing"
)

func resetLBFlags(t *testing.T) {
	t.Helper()
	resetFlags(t)
	*mode = "lb"
	*lbBackends = "10.0.0.1:9000:8080,10.0.0.2"
	*lbPolicy = "sticky"
	*backendInsecure = true
	*lbDynamic = true
	*lbRegisterTTL = 45
}

func TestParseBackends_FullSpec(t *testing.T) {
	resetLBFlags(t)
	*controlPort = "9000"
	*publicPort = "8080"

	backends, err := parseBackends("10.0.0.1:9001:8443,backend.example.com:9002:443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(backends))
	}
	if backends[0].controlAddr() != "10.0.0.1:9001" || backends[0].publicAddr() != "10.0.0.1:8443" {
		t.Fatalf("unexpected first backend: %+v", backends[0])
	}
	if backends[1].controlAddr() != "backend.example.com:9002" || backends[1].publicAddr() != "backend.example.com:443" {
		t.Fatalf("unexpected second backend: %+v", backends[1])
	}
}

func TestParseBackends_DefaultPorts(t *testing.T) {
	resetLBFlags(t)
	*controlPort = "9000"
	*publicPort = "8080"

	backends, err := parseBackends("10.0.0.3,10.0.0.4:9001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if backends[0].controlAddr() != "10.0.0.3:9000" || backends[0].publicAddr() != "10.0.0.3:8080" {
		t.Fatalf("unexpected host-only backend: %+v", backends[0])
	}
	if backends[1].controlAddr() != "10.0.0.4:9001" || backends[1].publicAddr() != "10.0.0.4:8080" {
		t.Fatalf("unexpected host+control backend: %+v", backends[1])
	}
}

func TestParseBackends_RejectsEmpty(t *testing.T) {
	resetLBFlags(t)
	if _, err := parseBackends(""); err == nil {
		t.Fatal("expected empty backends error")
	}
}

func TestValidateConfig_AcceptsLBMode(t *testing.T) {
	resetLBFlags(t)
	if err := validateConfig(); err != nil {
		t.Fatalf("expected valid lb config, got %v", err)
	}
}

func TestValidateConfig_RejectsInvalidLBPolicy(t *testing.T) {
	resetLBFlags(t)
	*lbPolicy = "random"
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "lb-policy") {
		t.Fatalf("expected lb-policy error, got %v", err)
	}
}

func TestLBManager_StickyRouteAssignment(t *testing.T) {
	backends, err := parseBackends("10.0.0.1:9000:8080,10.0.0.2:9000:8080")
	if err != nil {
		t.Fatalf("parse backends: %v", err)
	}
	m := newLBManager(backends)

	first, err := m.pickBackend("myapp")
	if err != nil {
		t.Fatalf("pick backend: %v", err)
	}
	m.registerRoute("myapp", first)

	second, err := m.pickBackend("myapp")
	if err != nil {
		t.Fatalf("pick backend again: %v", err)
	}
	if first.id != second.id {
		t.Fatalf("expected sticky assignment, got %s then %s", first.id, second.id)
	}
}

func TestLBManager_LeastLoadedAssignment(t *testing.T) {
	backends, err := parseBackends("10.0.0.1:9000:8080,10.0.0.2:9000:8080")
	if err != nil {
		t.Fatalf("parse backends: %v", err)
	}
	m := newLBManager(backends)
	m.incLoad(backends[0].id)

	chosen := m.pickLeastLoaded(backends)
	if chosen.id != backends[1].id {
		t.Fatalf("expected least-loaded backend %s, got %s", backends[1].id, chosen.id)
	}
}

func TestLBManager_RoundRobinAssignment(t *testing.T) {
	backends, err := parseBackends("10.0.0.1:9000:8080,10.0.0.2:9000:8080,10.0.0.3:9000:8080")
	if err != nil {
		t.Fatalf("parse backends: %v", err)
	}
	m := newLBManager(backends)
	*lbPolicy = "round-robin"

	seen := make(map[string]struct{})
	for i := 0; i < 6; i++ {
		b, err := m.pickBackend("new-sub")
		if err != nil {
			t.Fatalf("pick backend: %v", err)
		}
		seen[b.id] = struct{}{}
	}
	if len(seen) != 3 {
		t.Fatalf("expected all backends used, got %d", len(seen))
	}
}

func TestLBManager_RouteBackendLookup(t *testing.T) {
	backends, err := parseBackends("10.0.0.1:9000:8080")
	if err != nil {
		t.Fatalf("parse backends: %v", err)
	}
	m := newLBManager(backends)
	m.registerRoute("shop", backends[0])

	route, err := m.routeBackend("shop")
	if err != nil {
		t.Fatalf("route lookup: %v", err)
	}
	if route.id != backends[0].id {
		t.Fatalf("expected route to %s, got %s", backends[0].id, route.id)
	}
}

func TestBackendPublicScheme(t *testing.T) {
	resetLBFlags(t)
	b := &tunnelBackend{host: "10.0.0.1", controlPort: "9000", publicPort: "8080"}

	*prod = false
	*dev = false
	if b.publicScheme() != "http" {
		t.Fatalf("expected http scheme, got %s", b.publicScheme())
	}

	*dev = true
	if b.publicScheme() != "https" {
		t.Fatalf("expected https scheme in dev, got %s", b.publicScheme())
	}
}