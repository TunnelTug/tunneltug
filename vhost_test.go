package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHostMatchesVHostWildcardSubdomains(t *testing.T) {
	vh := VHostConfig{Domain: "motionkb.com", WildcardSubdomains: true}
	if !hostMatchesVHostEntry("my-portfolio.motionkb.com", vh) {
		t.Fatal("expected user subdomain to match")
	}
	if !hostMatchesVHostEntry("motionkb.com", vh) {
		t.Fatal("expected apex to match")
	}
	if !hostMatchesVHostEntry("www.motionkb.com", vh) {
		t.Fatal("expected www apex to match")
	}
	if hostMatchesVHostEntry("deep.sub.motionkb.com", vh) {
		t.Fatal("multi-level subdomain should not match")
	}
	if hostMatchesVHostEntry("myapp.tunneltug.com", vh) {
		t.Fatal("wrong domain should not match")
	}
	strict := VHostConfig{Domain: "tunneltug.com"}
	if hostMatchesVHostEntry("myapp.tunneltug.com", strict) {
		t.Fatal("tunnel product vhost must not match user subdomains")
	}
	if !hostMatchesVHost("tunneltug.com", "tunneltug.com") {
		t.Fatal("apex should match")
	}
	if !hostMatchesVHost("www.tunneltug.com", "tunneltug.com") {
		t.Fatal("www should match")
	}
}

func TestExpandCloudUpstream(t *testing.T) {
	id := VHostIdentity{CloudBackhaul: "http://10.0.0.1"}
	got := expandCloudUpstream(id, "cloud://8443")
	if got != "http://10.0.0.1:8443" {
		t.Fatalf("got %q", got)
	}
	if expandCloudUpstream(id, "http://127.0.0.1:3082") != "http://127.0.0.1:3082" {
		t.Fatal("plain upstream unchanged")
	}
}

func TestCollectVHostACMEDomains(t *testing.T) {
	file := VHostFile{
		ACMEDomains: []string{"williwaw.app"},
		VHosts: []VHostConfig{
			{Domain: "tunneltug.com"},
			{Domain: "motionkb.com", WildcardSubdomains: true},
		},
	}
	domains := collectVHostACMEDomains(file)
	want := map[string]bool{
		"williwaw.app":  true,
		"tunneltug.com": true,
		"motionkb.com":  true,
	}
	for _, d := range domains {
		if !want[d] {
			t.Fatalf("unexpected domain %q in %v", d, domains)
		}
		delete(want, d)
	}
	if len(want) != 0 {
		t.Fatalf("missing domains: %v", want)
	}
}

func TestBuildAndMatchVHostHandlers(t *testing.T) {
	id := VHostIdentity{}
	handlers := buildVHostHandlers(id, []VHostConfig{
		{Domain: "tunneltug.com", Upstream: "http://127.0.0.1:3082"},
		{Domain: "motionkb.com", Upstream: "http://127.0.0.1:3090", WildcardSubdomains: true},
	})
	if len(handlers) != 2 {
		t.Fatalf("handlers=%d", len(handlers))
	}
	if matchVHostHandler("tunneltug.com", []VHostConfig{{Domain: "tunneltug.com"}}, handlers) == nil {
		t.Fatal("apex match")
	}
	if matchVHostHandler("myapp.tunneltug.com", []VHostConfig{{Domain: "tunneltug.com"}}, handlers) != nil {
		t.Fatal("user tunnel subdomain must not match product vhost")
	}
	vhMotion := []VHostConfig{{Domain: "motionkb.com", WildcardSubdomains: true}}
	if matchVHostHandler("site.motionkb.com", vhMotion, handlers) == nil {
		t.Fatal("wildcard motion host")
	}
}

func TestLoadVHostsYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vhosts.yaml")
	content := `
platform_url: https://0trust.cloud
cloud_domain: 0trust.cloud
acme_domains:
  - tunneltug.com
vhosts:
  - domain: tunneltug.com
    upstream: http://127.0.0.1:3082
    auth_proxy: false
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	prev := *vhostsFile
	*vhostsFile = path
	t.Cleanup(func() {
		*vhostsFile = prev
		_ = loadVHosts()
	})

	if err := loadVHosts(); err != nil {
		t.Fatal(err)
	}
	if vhostCount() != 1 {
		t.Fatalf("count=%d", vhostCount())
	}
	h := matchProductVHost("tunneltug.com")
	if h == nil {
		t.Fatal("expected handler for tunneltug.com")
	}
	// Upstream is closed; still expect a response path (502 is fine).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://tunneltug.com/", nil)
	req.Host = "tunneltug.com"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway && rec.Code != http.StatusOK {
		// Reverse proxy to closed port is 502; accept that.
		if rec.Code < 400 {
			t.Fatalf("unexpected status %d", rec.Code)
		}
	}
}

func TestIsCloudAuthPath(t *testing.T) {
	for _, p := range []string{"/auth", "/auth/callback", "/samln/federate", "/.well-known/openid-configuration", "/api/v1/idp/session"} {
		if !isCloudAuthPath(p) {
			t.Fatalf("expected auth path %q", p)
		}
	}
	if isCloudAuthPath("/dashboard") {
		t.Fatal("dashboard is not auth path")
	}
}
