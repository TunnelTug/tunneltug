package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMinimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := `
node_id: test
tlds: [tunnel]
zone: tunneltug.tunnel
ns_host: ns.tunneltug.tunnel
dns:
  enabled: true
  listen: "127.0.0.1:15353"
  anycast_ip: "203.0.113.53"
bgp:
  backend: log
  next_hop: "203.0.113.53"
  prefixes:
    - "203.0.113.53/32"
health:
  dns_probe:
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLDs[0] != "tunnel" {
		t.Fatalf("tld: %v", cfg.TLDs)
	}
	if cfg.Health.DNSProbe.Target != "127.0.0.1:15353" {
		t.Fatalf("dns probe target default: %s", cfg.Health.DNSProbe.Target)
	}
	if len(cfg.Health.DNSProbe.Names) == 0 {
		t.Fatal("expected default probe names")
	}
}

func TestValidateRejectsICANNStyleMultiLabelTLD(t *testing.T) {
	cfg := &Config{
		TLDs: []string{"co.uk"},
		BGP: BGPConfig{
			Backend:  "log",
			NextHop:  "203.0.113.53",
			Prefixes: []string{"203.0.113.53/32"},
		},
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected multi-label tld rejection")
	}
}

func TestValidateRequiresPrefix(t *testing.T) {
	cfg := &Config{
		TLDs: []string{"tunnel"},
		BGP: BGPConfig{
			Backend: "log",
			NextHop: "203.0.113.53",
		},
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing prefixes error")
	}
}

func TestIBGPDefaultsPeerASN(t *testing.T) {
	cfg := &Config{
		TLDs: []string{"tunnel"},
		BGP: BGPConfig{
			Backend:  "log",
			NextHop:  "203.0.113.53",
			Prefixes: []string{"203.0.113.53/32"},
			IBGP:     true,
			LocalASN: 65000,
			PeerASN:  0,
		},
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.BGP.PeerASN != 65000 {
		t.Fatalf("peer_asn=%d", cfg.BGP.PeerASN)
	}
}

func TestLoadShadowExample(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))
	path := "config/shadow.example.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Skip("shadow.example.yaml not found")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNS.ZonePack == "" {
		t.Fatal("expected dns.zone_pack")
	}
	if !cfg.Origin.Enabled {
		t.Fatal("expected origin enabled")
	}
	if !contains(cfg.DNS.PrivateSuffixes, ".com") {
		t.Fatalf("suffixes=%v", cfg.DNS.PrivateSuffixes)
	}
}
