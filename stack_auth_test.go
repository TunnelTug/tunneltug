package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStackOperatorMeshHost(t *testing.T) {
	prev := *meshZone
	*meshZone = "tunneltug.tunnel"
	defer func() { *meshZone = prev }()
	if got := stackOperatorMeshHost(); got != "stack.tunneltug.tunnel" {
		t.Fatalf("host %q", got)
	}
}

func TestStackMeshOperatorOriginIsHTTPSMesh(t *testing.T) {
	prevZ, prevP := *meshZone, *meshTLSPort
	*meshZone = "tunneltug.tunnel"
	*meshTLSPort = "4073"
	defer func() { *meshZone = prevZ; *meshTLSPort = prevP }()
	o := stackMeshOperatorOrigin()
	if !strings.HasPrefix(o, "https://stack.tunneltug.tunnel") {
		t.Fatalf("origin %q", o)
	}
	if strings.Contains(o, "127.0.0.1") || strings.Contains(o, "localhost") || strings.HasPrefix(o, "http://") {
		t.Fatalf("must not be loopback HTTP: %q", o)
	}
}

func TestStackAuthLoginURL(t *testing.T) {
	prevZ, prevP := *meshZone, *meshTLSPort
	*meshZone = "tunneltug.tunnel"
	*meshTLSPort = "4073"
	defer func() { *meshZone = prevZ; *meshTLSPort = prevP }()
	u := stackAuthLoginURL("/shell?app=platform")
	if !strings.HasPrefix(u, "https://") {
		t.Fatalf("login must be https: %s", u)
	}
	if strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") {
		t.Fatalf("login must not use localhost: %s", u)
	}
	if !strings.Contains(u, "/auth") || !strings.Contains(u, "return_to=") {
		t.Fatalf("login url: %s", u)
	}
}

func TestRequestIsMeshAuthFace(t *testing.T) {
	prev := *meshZone
	*meshZone = "tunneltug.tunnel"
	defer func() { *meshZone = prev }()

	// Loopback HTTP — reject
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4070/shell", nil)
	req.Host = "127.0.0.1:4070"
	if requestIsMeshAuthFace(req) {
		t.Fatal("loopback HTTP must not be mesh auth face")
	}

	// Mesh HTTPS — accept
	req2 := httptest.NewRequest(http.MethodGet, "https://stack.tunneltug.tunnel:4073/shell", nil)
	req2.Host = "stack.tunneltug.tunnel:4073"
	req2.TLS = &tls.ConnectionState{}
	if !requestIsMeshAuthFace(req2) {
		t.Fatal("mesh HTTPS should be auth face")
	}
}

func TestRequireStackAuthRejectsLoopback(t *testing.T) {
	prev := *stackAuthProxy
	*stackAuthProxy = true
	defer func() { *stackAuthProxy = prev }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4070/shell?app=platform", nil)
	req.Host = "127.0.0.1:4070"
	req.Header.Set("Accept", "text/html")
	_, ok := requireStackAuth(rr, req)
	if ok {
		t.Fatal("loopback must be denied")
	}
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://") || strings.Contains(loc, "127.0.0.1") {
		t.Fatalf("redirect should be mesh https: %s", loc)
	}
}

func TestRequireStackAuthDisabled(t *testing.T) {
	prev := *stackAuthProxy
	*stackAuthProxy = false
	defer func() { *stackAuthProxy = prev }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/shell?app=x", nil)
	sub, ok := requireStackAuth(rr, req)
	if !ok || sub != "auth-proxy-off" {
		t.Fatalf("disabled gate: ok=%v sub=%q status=%d", ok, sub, rr.Code)
	}
}

func TestRequireStackAuthOnMeshMissingSession(t *testing.T) {
	prevA, prevZ := *stackAuthProxy, *meshZone
	*stackAuthProxy = true
	*meshZone = "tunneltug.tunnel"
	defer func() { *stackAuthProxy = prevA; *meshZone = prevZ }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://stack.tunneltug.tunnel:4073/shell?app=platform", nil)
	req.Host = "stack.tunneltug.tunnel:4073"
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("Accept", "text/html")
	_, ok := requireStackAuth(rr, req)
	if ok {
		t.Fatal("expected deny without session")
	}
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "/auth") || !strings.HasPrefix(loc, "https://") {
		t.Fatalf("Location %q", loc)
	}
}

func TestRequireStackAuthJSONWhenAPI(t *testing.T) {
	prevA, prevZ := *stackAuthProxy, *meshZone
	*stackAuthProxy = true
	*meshZone = "tunneltug.tunnel"
	defer func() { *stackAuthProxy = prevA; *meshZone = prevZ }()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://stack.tunneltug.tunnel:4073/shell/ws?app=platform", nil)
	req.Host = "stack.tunneltug.tunnel:4073"
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Upgrade", "websocket")
	_, ok := requireStackAuth(rr, req)
	if ok {
		t.Fatal("expected deny")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("body %v", body)
	}
}

func TestStackTrustedMeshIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/shell", nil)
	req.Header.Set("X-0Trust-Subject", "alice@example.com")
	req.Header.Set("X-0Trust-DBSC", "bound")
	sub, ok := stackTrustedMeshIdentity(req)
	if !ok || sub != "alice@example.com" {
		t.Fatalf("got %q ok=%v", sub, ok)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/shell", nil)
	req2.Header.Set("X-0Trust-Subject", "alice@example.com")
	if _, ok := stackTrustedMeshIdentity(req2); ok {
		t.Fatal("should reject unbound mesh subject")
	}
}

func TestStackSessionDBSCBound(t *testing.T) {
	if stackSessionDBSCBound(stackIDPSession{Valid: true}) {
		t.Fatal("valid without bind should fail")
	}
	if !stackSessionDBSCBound(stackIDPSession{Valid: true, DBSCBound: true}) {
		t.Fatal("dbsc_bound should pass")
	}
}
