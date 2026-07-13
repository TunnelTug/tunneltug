package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateConfig_AcceptsLBWithDynamicOnly(t *testing.T) {
	resetLBFlags(t)
	*lbBackends = ""
	*lbDynamic = true
	if err := validateConfig(); err != nil {
		t.Fatalf("expected valid dynamic lb config, got %v", err)
	}
}

func TestValidateConfig_RejectsLBWithoutBackendsOrDynamic(t *testing.T) {
	resetLBFlags(t)
	*lbBackends = ""
	*lbDynamic = false
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("expected backend requirement error, got %v", err)
	}
}

func TestLBManager_DynamicRegisterAndDeregister(t *testing.T) {
	resetLBFlags(t)
	*authToken = "this-is-a-valid-token"
	*lbDynamic = true

	m := newLBManager(nil)
	req := lbRegisterRequest{
		Token:       *authToken,
		Host:        "10.0.0.8",
		ControlPort: "9100",
		PublicPort:  "8180",
		FleetID:     "fleet-a-0",
	}

	backend, err := m.registerDynamicBackend(req)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if backend.id != "10.0.0.8:9100" || !backend.dynamic || backend.namespace != defaultNamespace {
		t.Fatalf("unexpected backend: %+v", backend)
	}

	if !m.deregisterDynamicBackend(backend.id) {
		t.Fatal("expected deregister to succeed")
	}
	if len(m.backends) != 0 {
		t.Fatalf("expected no backends after deregister, got %d", len(m.backends))
	}
}

func TestLBManager_PruneStaleDynamicBackends(t *testing.T) {
	resetLBFlags(t)
	*lbRegisterTTL = 10

	m := newLBManager(nil)
	stale := &tunnelBackend{
		id:          "10.0.0.9:9200",
		host:        "10.0.0.9",
		controlPort: "9200",
		publicPort:  "8280",
		dynamic:     true,
		lastSeen:    time.Now().Add(-30 * time.Second),
	}
	fresh := &tunnelBackend{
		id:          "10.0.0.10:9200",
		host:        "10.0.0.10",
		controlPort: "9200",
		publicPort:  "8280",
		dynamic:     true,
		lastSeen:    time.Now(),
	}
	m.backends = []*tunnelBackend{stale, fresh}

	m.pruneStaleBackends()

	if len(m.backends) != 1 || m.backends[0].id != fresh.id {
		t.Fatalf("expected only fresh backend, got %+v", m.backends)
	}
}

func TestLBManager_EligibleBackendsSkipsStale(t *testing.T) {
	resetLBFlags(t)
	*lbRegisterTTL = 10

	m := newLBManager(nil)
	stale := &tunnelBackend{
		id: "10.0.0.11:9300", dynamic: true, lastSeen: time.Now().Add(-30 * time.Second),
	}
	fresh := &tunnelBackend{
		id: "10.0.0.12:9300", dynamic: true, lastSeen: time.Now(),
	}
	m.backends = []*tunnelBackend{stale, fresh}

	eligible := m.eligibleBackends(m.backends)
	if len(eligible) != 1 || eligible[0].id != fresh.id {
		t.Fatalf("expected only fresh backend eligible, got %+v", eligible)
	}
}

func TestLBRegisterHandler_AuthAndSuccess(t *testing.T) {
	resetLBFlags(t)
	*authToken = "this-is-a-valid-token"
	*lbDynamic = true

	m := newLBManager(nil)
	mux := http.NewServeMux()
	m.mountRegisterHandlers(mux)

	body, _ := json.Marshal(lbRegisterRequest{
		Token:       *authToken,
		Host:        "10.0.0.20",
		ControlPort: "9400",
		PublicPort:  "8480",
		FleetID:     "fleet-b-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/_tunneltug/lb/register", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(m.backends) != 1 {
		t.Fatalf("expected registered backend, got %d", len(m.backends))
	}
}

func TestLBRegisterHandler_RejectsBadToken(t *testing.T) {
	resetLBFlags(t)
	*authToken = "this-is-a-valid-token"
	*lbDynamic = true

	m := newLBManager(nil)
	mux := http.NewServeMux()
	m.mountRegisterHandlers(mux)

	body, _ := json.Marshal(lbRegisterRequest{
		Token:       "wrong-token-value",
		Host:        "10.0.0.21",
		ControlPort: "9500",
		PublicPort:  "8580",
	})
	req := httptest.NewRequest(http.MethodPost, "/_tunneltug/lb/register", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}