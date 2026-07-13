package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeshPrivateName(t *testing.T) {
	*meshZone = "tunneltug.tunnel"
	if got := meshPrivateName("myapp"); got != "myapp.tunneltug.tunnel" {
		t.Fatalf("meshPrivateName(myapp) = %q", got)
	}
	if got := meshPrivateName("myapp.tunneltug.tunnel"); got != "myapp.tunneltug.tunnel" {
		t.Fatalf("meshPrivateName full = %q", got)
	}
	if got := meshPrivateName(""); got != "tunneltug.tunnel" {
		t.Fatalf("meshPrivateName empty = %q", got)
	}
}

func TestMeshHostID_Direct(t *testing.T) {
	t.Cleanup(func() {
		*routing = "subdomain"
		*meshHost = ""
	})
	*meshHost = ""
	*subdomain = "myapp"
	*routing = "direct"
	if got := meshHostID(); got != "direct" {
		t.Fatalf("direct meshHostID = %q, want direct", got)
	}
	*routing = "subdomain"
	if got := meshHostID(); got != "myapp" {
		t.Fatalf("subdomain meshHostID = %q, want myapp", got)
	}
}

func TestIsMeshPrivateName(t *testing.T) {
	*meshTLD = "tunnel"
	if !isMeshPrivateName("myapp.tunneltug.tunnel") {
		t.Fatal("expected private .tunnel name")
	}
	if !isMeshPrivateName("foo.mesh") {
		t.Fatal("expected private .mesh name")
	}
	if isMeshPrivateName("example.com") {
		t.Fatal("public name should not be private")
	}
}

func TestValidateConfig_MeshZone(t *testing.T) {
	resetFlags(t)
	*meshEnabled = true
	*meshTLD = "tunnel"
	*meshZone = "badzone"
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "mesh-zone") {
		t.Fatalf("expected mesh-zone error, got %v", err)
	}

	*meshZone = "tunneltug.mesh"
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "mesh-tld") {
		t.Fatalf("expected mesh-tld mismatch error, got %v", err)
	}

	*meshZone = "tunneltug.tunnel"
	if err := validateConfig(); err != nil {
		t.Fatalf("valid mesh config: %v", err)
	}
	*meshEnabled = false
}

func TestValidateConfig_DirectProdClient(t *testing.T) {
	resetFlags(t)
	*mode = "client"
	*routing = "direct"
	*prod = true
	*domain = "example.com"
	*authToken = "this-is-a-long-enough-prod-token"
	*serverIP = "127.0.0.1"
	// Still valid: domain is set; applyProductionDefaults rewrites serverIP.
	if err := validateConfig(); err != nil {
		t.Fatalf("direct prod client should be valid: %v", err)
	}
	applyProductionDefaults()
	if *serverIP != "example.com" {
		t.Fatalf("expected serverIP rewritten to domain, got %q", *serverIP)
	}
	if *publicPort != "443" {
		t.Fatalf("expected public port 443, got %q", *publicPort)
	}
}

func TestPublicURL_DirectProd(t *testing.T) {
	t.Cleanup(func() {
		*prod = false
		*routing = "subdomain"
		*publicPort = "8080"
	})
	*prod = true
	*dev = false
	*publicPort = "443"
	*serverIP = "example.com"
	setRoutingFlags(t, "direct", "example.com")
	want := "https://example.com"
	if got := publicURL(); got != want {
		t.Fatalf("publicURL() = %q, want %q", got, want)
	}
}

func TestTLSHosts_DirectIncludesWWW(t *testing.T) {
	t.Cleanup(func() {
		*prod = false
		*routing = "subdomain"
		*domain = ""
	})
	*prod = true
	*routing = "direct"
	*domain = "example.com"
	*subalt = ""
	*vhostsFile = ""
	hosts := tlsHosts()
	foundWWW := false
	foundApex := false
	for _, h := range hosts {
		if h == "example.com" {
			foundApex = true
		}
		if h == "www.example.com" {
			foundWWW = true
		}
	}
	if !foundApex || !foundWWW {
		t.Fatalf("tlsHosts direct = %v, want apex + www", hosts)
	}
}

func TestMeshAuthority_PublishAndLookup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mesh-state")
	*meshEnabled = true
	*mode = "server"
	*routing = "subdomain"
	*meshDataDir = dir
	*meshEdgeIP = "10.0.0.9"
	*meshTLD = "tunnel"
	*meshZone = "tunneltug.tunnel"
	*meshNSHost = "ns.tunneltug.tunnel"
	*meshDNS = "127.0.0.1:" + freeUDPPort(t)

	auth, err := startMeshAuthority()
	if err != nil {
		t.Fatalf("startMeshAuthority: %v", err)
	}
	if auth == nil {
		t.Fatal("expected mesh authority")
	}
	t.Cleanup(func() {
		auth.Close()
		*meshEnabled = false
		*routing = "subdomain"
	})

	name, err := auth.PublishTunnel("demo", "10.0.0.9")
	if err != nil {
		t.Fatalf("PublishTunnel: %v", err)
	}
	if name != "demo.tunneltug.tunnel" {
		t.Fatalf("published name = %q", name)
	}

	// Second publish is idempotent.
	if _, err := auth.PublishTunnel("demo", "10.0.0.9"); err != nil {
		t.Fatalf("republish: %v", err)
	}

	recs, err := auth.Lookup("demo.tunneltug.tunnel")
	if err != nil || len(recs) == 0 {
		t.Fatalf("lookup: %v recs=%v", err, recs)
	}
	if recs[0].Value != "10.0.0.9" {
		t.Fatalf("A record = %q, want 10.0.0.9", recs[0].Value)
	}

	// Identity file should persist.
	if _, err := os.Stat(filepath.Join(dir, "identity.pub")); err != nil {
		t.Fatalf("identity not persisted: %v", err)
	}

	// HTTP API
	mux := http.NewServeMux()
	mountMeshHandlers(mux)
	req := httptest.NewRequest(http.MethodGet, "/_tunneltug/mesh/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status code %d", rr.Code)
	}
	var status map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["enabled"] != true {
		t.Fatalf("status enabled = %v", status["enabled"])
	}

	// Close before temp-dir teardown so Windows releases mesh.db handles.
	auth.Close()
}

func freeUDPPort(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer pc.Close()
	_, port, err := net.SplitHostPort(pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	return port
}
