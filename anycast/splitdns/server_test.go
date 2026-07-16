package splitdns

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0TrustCloud/secure_dns"

	"tunneltug/anycast/config"
)

func TestIsPrivate(t *testing.T) {
	suffixes := []string{".tunnel", ".mesh", ".com"}
	cases := []struct {
		name string
		want bool
	}{
		{"app.tunneltug.tunnel", true},
		{"tunnel", true},
		{"example.com", true},
		{"foo.bar.example.com", true},
		{"foo.mesh", true},
		{"other.example", false},
	}
	for _, tc := range cases {
		if got := isPrivate(tc.name, suffixes); got != tc.want {
			t.Errorf("isPrivate(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBootstrapAndSnapshot(t *testing.T) {
	cfg := &config.Config{
		TLDs:   []string{"tunnel"},
		Zone:   "tunneltug.tunnel",
		NSHost: "ns.tunneltug.tunnel",
		DNS: config.DNSConfig{
			Enabled:         true,
			Listen:          "127.0.0.1:0",
			AnycastIP:       "203.0.113.53",
			PrivateSuffixes: []string{".tunnel"},
			Recursive:       []string{"1.1.1.1:53"},
		},
		BGP: config.BGPConfig{NextHop: "203.0.113.53"},
	}
	s := New(cfg)
	s.BootstrapSeeds()
	if s.auth.RecordCount() == 0 {
		t.Fatal("expected bootstrap records")
	}

	snap := secure_dns.ZoneSnapshot{
		Host: "ns.tunneltug.tunnel",
		Records: []secure_dns.DNSRecord{
			{Domain: "myapp.tunneltug.tunnel", Type: "A", Value: "198.51.100.10", TTL: 60},
		},
		PrivateSuffixes: []string{".tunnel", ".factory"},
	}
	s.LoadSnapshot(snap)
	if !isPrivate("x.factory", s.privateSuffixes()) {
		t.Fatal("expected .factory from snapshot suffixes")
	}
	if s.auth.RecordCount() == 0 {
		t.Fatal("expected records after snapshot")
	}
}

func TestBootstrapZonePack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zone.json")
	raw := `{
  "host": "ns.shadow",
  "private_suffixes": [".com"],
  "records": [
    {"domain": "www.example.com", "type": "A", "value": "{{VIP}}", "ttl": 60}
  ]
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		TLDs:   []string{"tunnel"},
		Zone:   "tunneltug.tunnel",
		NSHost: "ns.shadow",
		DNS: config.DNSConfig{
			Enabled:         true,
			Listen:          "127.0.0.1:0",
			AnycastIP:       "203.0.113.53",
			PrivateSuffixes: []string{".tunnel", ".com"},
			ZonePack:        path,
			Recursive:       []string{"1.1.1.1:53"},
		},
		BGP: config.BGPConfig{NextHop: "203.0.113.53"},
	}
	s := New(cfg)
	s.BootstrapSeeds()
	if !isPrivate("www.example.com", s.privateSuffixes()) {
		t.Fatalf("suffixes=%v", s.privateSuffixes())
	}
	if s.auth.RecordCount() < 2 {
		t.Fatalf("records=%d", s.auth.RecordCount())
	}
	st := s.Status()
	if st["zone_pack"] == "" {
		t.Fatal("expected zone_pack in status")
	}
}
