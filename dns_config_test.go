package main

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDNSFile_RequiresZoneMatch(t *testing.T) {
	err := validateDNSFile(DNSFile{Zones: []DNSZoneConfig{{DoH: "https://dns.example/dns-query"}}})
	if err == nil || !strings.Contains(err.Error(), "tld and/or domains") {
		t.Fatalf("expected tld/domains error, got %v", err)
	}
}

func TestValidateDNSFile_RejectsBadDoH(t *testing.T) {
	err := validateDNSFile(DNSFile{Zones: []DNSZoneConfig{{
		TLD: "corp",
		DoH: "not-a-url",
	}}})
	if err == nil {
		t.Fatal("expected doh validation error")
	}
}

func TestValidateDNSFile_AcceptsTLDWithLeadingDot(t *testing.T) {
	f := DNSFile{Zones: []DNSZoneConfig{{
		TLD: ".corp",
		DoH: "https://dns.corp.example/dns-query",
	}}}
	if err := validateDNSFile(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMatchDNSZone_Specificity(t *testing.T) {
	zones := []DNSZoneConfig{
		{TLD: "corp", DoH: "https://tld.example/dns-query"},
		{Domains: []string{"*.lab.corp"}, DoH: "https://lab.example/dns-query"},
		{Domains: []string{"api.lab.corp"}, DoH: "https://api.example/dns-query"},
	}

	cases := []struct {
		name string
		want string
	}{
		{"api.lab.corp", "https://api.example/dns-query"},
		{"x.lab.corp", "https://lab.example/dns-query"},
		{"other.corp", "https://tld.example/dns-query"},
		{"corp", "https://tld.example/dns-query"},
		{"public.example", ""},
	}
	for _, tc := range cases {
		z, ok := matchDNSZone(tc.name, zones)
		if tc.want == "" {
			if ok {
				t.Fatalf("%s: expected no match, got %#v", tc.name, z)
			}
			continue
		}
		if !ok || z.DoH != tc.want {
			t.Fatalf("%s: got doh=%q ok=%v, want %q", tc.name, z.DoH, ok, tc.want)
		}
	}
}

func TestResolverForDomain_UsesZoneDoH(t *testing.T) {
	prevFlag := *dnsFileFlag
	t.Cleanup(func() {
		*dnsFileFlag = prevFlag
		dnsMu.Lock()
		dnsFile = DNSFile{}
		dnsLoaded = false
		dnsMu.Unlock()
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "dns.yaml")
	content := `
listen: 127.0.0.1:5359
fallback: 1.1.1.1:53
zones:
  - tld: corp
    doh: https://dns.corp.example/dns-query
  - domains:
      - "*.lab.internal"
    doh: https://lab.example/dns-query
    upstream: 10.0.0.53:53
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	*dnsFileFlag = path
	if err := loadDNSConfig(); err != nil {
		t.Fatalf("loadDNSConfig: %v", err)
	}

	cfg := vpiStubConfig{
		UpstreamNS:      "127.0.0.1:5353",
		FallbackNS:      "8.8.8.8:53",
		PrivateSuffixes: []string{".corp", ".internal", ".tunnel"},
	}

	r := resolverForDomain("app.corp", cfg)
	if r.DoH != "https://dns.corp.example/dns-query" {
		t.Fatalf("corp doh = %q", r.DoH)
	}

	r = resolverForDomain("x.lab.internal", cfg)
	if r.DoH != "https://lab.example/dns-query" || r.UDP != "10.0.0.53:53" {
		t.Fatalf("lab resolver = %#v", r)
	}

	r = resolverForDomain("example.com", cfg)
	if r.UDP != "1.1.1.1:53" && r.DoH != "" {
		// fallback from yaml is classic
	}
	if r.UDP != "1.1.1.1:53" {
		t.Fatalf("fallback = %#v", r)
	}
}

func TestLoadDNSConfig_JSON(t *testing.T) {
	prevFlag := *dnsFileFlag
	t.Cleanup(func() {
		*dnsFileFlag = prevFlag
		dnsMu.Lock()
		dnsFile = DNSFile{}
		dnsLoaded = false
		dnsMu.Unlock()
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "dns.json")
	content := `{"zones":[{"tld":"mesh","doh":"https://doh.example/dns-query"}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	*dnsFileFlag = path
	if err := loadDNSConfig(); err != nil {
		t.Fatalf("load: %v", err)
	}
	f := getDNSFile()
	if len(f.Zones) != 1 || f.Zones[0].TLD != "mesh" {
		t.Fatalf("unexpected file: %#v", f)
	}
}

func TestVPIExchangeDoH_POST(t *testing.T) {
	// Minimal DNS response: copy query header + QR bit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/dns-message" {
			t.Errorf("content-type %s", ct)
		}
		body := make([]byte, 512)
		n, _ := r.Body.Read(body)
		if n < 12 {
			http.Error(w, "short", 400)
			return
		}
		resp := make([]byte, n)
		copy(resp, body[:n])
		// Set QR (response) bit
		flags := binary.BigEndian.Uint16(resp[2:4])
		flags |= 0x8000
		binary.BigEndian.PutUint16(resp[2:4], flags)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	// Build a tiny A query for example.com
	q := buildTestDNSQuery("example.com")
	out, err := vpiExchangeDoH(q, srv.URL, "post")
	if err != nil {
		t.Fatalf("doh: %v", err)
	}
	if len(out) < 12 {
		t.Fatalf("short response %d", len(out))
	}
	if binary.BigEndian.Uint16(out[2:4])&0x8000 == 0 {
		t.Fatal("expected QR bit set")
	}
}

func TestPrivateSuffixesFromDNSFile(t *testing.T) {
	f := DNSFile{
		PrivateTLDs: []string{"internal"},
		Zones: []DNSZoneConfig{
			{TLD: "corp"},
			{Domains: []string{"*.lab.example"}},
		},
	}
	suf := privateSuffixesFromDNSFile(f)
	want := map[string]bool{".internal": true, ".corp": true, ".example": true}
	for _, s := range suf {
		if !want[s] {
			t.Fatalf("unexpected suffix %q in %v", s, suf)
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Fatalf("missing suffixes %v", want)
	}
}

func TestDNSConfigActivatesVPI(t *testing.T) {
	prevDNS := *dnsFileFlag
	prevVPI := *vpiStub
	prevMesh := *meshEnabled
	t.Cleanup(func() {
		*dnsFileFlag = prevDNS
		*vpiStub = prevVPI
		*meshEnabled = prevMesh
	})
	*meshEnabled = false
	*vpiStub = false
	*dnsFileFlag = ""
	if vpiActive() {
		t.Fatal("expected vpi inactive")
	}
	*dnsFileFlag = "config/dns.yaml"
	if !vpiActive() {
		t.Fatal("expected vpi active with -dns")
	}
}

// buildTestDNSQuery crafts a minimal standard DNS query packet.
func buildTestDNSQuery(name string) []byte {
	var b []byte
	// ID + flags (RD) + counts
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint16(hdr[0:2], 0x1234)
	binary.BigEndian.PutUint16(hdr[2:4], 0x0100) // RD
	binary.BigEndian.PutUint16(hdr[4:6], 1)      // QDCOUNT
	b = append(b, hdr...)
	for _, label := range strings.Split(name, ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	// QTYPE A, QCLASS IN
	b = append(b, 0, 1, 0, 1)
	return b
}
