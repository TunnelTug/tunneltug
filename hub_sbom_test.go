package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateCycloneDXFromBinary(t *testing.T) {
	// Use the currently running test binary as a Go binary with buildinfo.
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := generateCycloneDXFromBinary(bin, "0trust/bandy", "latest")
	if err != nil {
		t.Fatal(err)
	}
	var bom cdxBOM
	if err := json.Unmarshal(raw, &bom); err != nil {
		t.Fatal(err)
	}
	if bom.BOMFormat != "CycloneDX" {
		t.Fatalf("bomFormat %q", bom.BOMFormat)
	}
	if bom.SpecVersion != cycloneDXSpec {
		t.Fatalf("spec %q", bom.SpecVersion)
	}
	if bom.Metadata.Component.Name != "bandy" {
		t.Fatalf("component name %q", bom.Metadata.Component.Name)
	}
	if len(bom.Metadata.Component.Hashes) == 0 {
		t.Fatal("expected SHA-256 hash")
	}
	// Go test binaries usually embed module deps.
	if runtime.GOOS != "" && len(bom.Components) == 0 {
		t.Log("warning: no deps in buildinfo (ok for some test binaries)")
	}
}

func TestHubSBOMKey(t *testing.T) {
	if got := hubSBOMKey("0trust/bandy", "latest"); got != "sboms/0trust/bandy/latest.cdx.json" {
		t.Fatalf("key %q", got)
	}
}

func TestSBOMPutGetAndView(t *testing.T) {
	h, err := newHubServer(hubConfig{Public: "https://hub.tunneltug.com", Token: "test-token", Memory: true})
	if err != nil {
		t.Fatal(err)
	}
	// Put SBOM
	body := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"metadata":{"component":{"type":"application","name":"bandy","version":"latest"}},"components":[]}`)
	req := httptest.NewRequest(http.MethodPut, "/_tunneltug/hub/sbom/0trust/bandy?tag=latest", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.handleSBOM(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("put status %d body %s", rec.Code, rec.Body.String())
	}

	// Get JSON
	req = httptest.NewRequest(http.MethodGet, "/_tunneltug/hub/sbom/0trust/bandy?tag=latest", nil)
	rec = httptest.NewRecorder()
	h.handleSBOM(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CycloneDX") {
		t.Fatalf("body %s", rec.Body.String())
	}

	// View HTML
	req = httptest.NewRequest(http.MethodGet, "/_tunneltug/hub/sbom/0trust/bandy/view?tag=latest", nil)
	rec = httptest.NewRecorder()
	h.handleSBOM(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("view status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "Software Bill of Materials") {
		t.Fatalf("view missing title: %s", rec.Body.String()[:min(200, rec.Body.Len())])
	}

	// Catalog
	req = httptest.NewRequest(http.MethodGet, "/_tunneltug/hub/sboms", nil)
	rec = httptest.NewRecorder()
	h.handleSBOMCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "0trust/bandy") {
		t.Fatalf("catalog %s", rec.Body.String())
	}
}

func TestSBOMUnauthorizedPut(t *testing.T) {
	h, err := newHubServer(hubConfig{Public: "https://hub.tunneltug.com", Token: "secret", Memory: true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/_tunneltug/hub/sbom/0trust/ack?tag=latest",
		strings.NewReader(`{"bomFormat":"CycloneDX","components":[]}`))
	rec := httptest.NewRecorder()
	h.handleSBOM(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestGenerateFromTempFile(t *testing.T) {
	// Non-Go binary still produces a minimal SBOM with hash.
	dir := t.TempDir()
	p := filepath.Join(dir, "product")
	if err := os.WriteFile(p, []byte("not-a-go-binary-but-ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := generateCycloneDXFromBinary(p, "0trust/ack", "v1")
	if err != nil {
		t.Fatal(err)
	}
	var bom cdxBOM
	if err := json.Unmarshal(raw, &bom); err != nil {
		t.Fatal(err)
	}
	if bom.Metadata.Component.Version != "v1" {
		t.Fatalf("version %q", bom.Metadata.Component.Version)
	}
}
