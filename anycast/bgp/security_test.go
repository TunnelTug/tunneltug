package bgp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tunneltug/anycast/bgpsec"
	"tunneltug/anycast/config"
)

func TestManagerSignsAndGates(t *testing.T) {
	pem, ski, err := bgpsec.GenerateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "router.key")
	if err := os.WriteFile(keyPath, []byte(pem), 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(t.TempDir(), "sigs.jsonl")
	routesPath := filepath.Join(t.TempDir(), "routes")
	fc := true
	cfg := &config.Config{
		TLDs: []string{"tunnel"},
		BGP: config.BGPConfig{
			Backend:  "file",
			LocalASN: 65001,
			PeerASN:  65000,
			NextHop:  "203.0.113.53",
			Prefixes: []string{"203.0.113.53/32"},
			File:     config.FileConfig{Path: routesPath},
			Security: config.SecurityConfig{
				FailClosed: &fc,
				ROV: config.ROVConfig{
					Enabled:      true,
					RequireValid: true,
					AllowPrivate: true,
				},
				BGPsec: config.BGPsecConfig{
					Enabled:       true,
					RequireSign:   true,
					PrivateKey:    keyPath,
					SKI:           ski,
					TargetASN:     65000,
					SignatureFile: sigPath,
				},
			},
		},
	}
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetHealthy(true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(routesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(raw), "203.0.113.53/32", "bgpsec_ski", "bgpsec_sig") {
		t.Fatalf("expected signed route file: %s", raw)
	}
	sigRaw, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sigRaw) < 20 {
		t.Fatalf("signature export empty: %s", sigRaw)
	}
	st := m.Status()
	sec, _ := st["security"].(map[string]any)
	if sec == nil {
		t.Fatal("no security in status")
	}
	if err := m.SetHealthy(false); err != nil {
		t.Fatal(err)
	}
}

func TestManagerROVRejects(t *testing.T) {
	pem, _, err := bgpsec.GenerateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "router.key")
	_ = os.WriteFile(keyPath, []byte(pem), 0o600)
	fc := true
	cfg := &config.Config{
		TLDs: []string{"tunnel"},
		BGP: config.BGPConfig{
			Backend:  "log",
			LocalASN: 64496, // public-ish lab ASN without private range for allow_private path on prefix only
			PeerASN:  65000,
			NextHop:  "198.51.100.1",
			Prefixes: []string{"198.51.100.0/24"},
			Security: config.SecurityConfig{
				FailClosed: &fc,
				ROV: config.ROVConfig{
					Enabled:      true,
					RequireValid: true,
					AllowPrivate: false,
					ROAs: []config.ROAEntry{{
						Prefix:    "198.51.100.0/24",
						ASN:       64500, // different ASN
						MaxLength: 24,
					}},
				},
				BGPsec: config.BGPsecConfig{
					Enabled:     true,
					RequireSign: true,
					PrivateKey:  keyPath,
					TargetASN:   65000,
				},
			},
		},
	}
	// 64496 is not private ASN; DOC prefix alone without allow_private - ROA wrong ASN → invalid
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetHealthy(true); err == nil {
		t.Fatal("expected ROV reject")
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
