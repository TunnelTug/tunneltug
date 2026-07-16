package rov

import (
	"os"
	"path/filepath"
	"testing"
)

func TestROVValidROA(t *testing.T) {
	v, err := New(Config{
		Enabled:      true,
		RequireValid: true,
		ROAs: []ROA{{
			Prefix:    "203.0.113.0/24",
			ASN:       64496,
			MaxLength: 32,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, val, _ := v.AllowAnnounce("203.0.113.53/32", 64496)
	if !ok || val != Valid {
		t.Fatalf("want valid, ok=%v val=%v", ok, val)
	}
	ok, val, _ = v.AllowAnnounce("203.0.113.53/32", 65000)
	if ok || val != Invalid {
		t.Fatalf("want invalid other asn, ok=%v val=%v", ok, val)
	}
}

func TestROVAllowPrivateLab(t *testing.T) {
	v, err := New(Config{
		Enabled:      true,
		RequireValid: true,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, val, _ := v.AllowAnnounce("203.0.113.53/32", 65001)
	if !ok || val != Valid {
		t.Fatalf("lab private: ok=%v val=%v", ok, val)
	}
}

func TestROVFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roas.json")
	raw := `[{"prefix":"198.51.100.0/24","asn":64500,"max_length":32}]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := New(Config{Enabled: true, RequireValid: true, ROAFile: path})
	if err != nil {
		t.Fatal(err)
	}
	ok, _, _ := v.AllowAnnounce("198.51.100.10/32", 64500)
	if !ok {
		t.Fatal("expected allow")
	}
}

func TestDisabled(t *testing.T) {
	v, err := New(Config{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	ok, val, _ := v.AllowAnnounce("8.8.8.8/32", 15169)
	if !ok || val != Valid {
		t.Fatalf("disabled should allow: %v %v", ok, val)
	}
}
