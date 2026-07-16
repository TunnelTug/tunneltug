package bgpsec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSignOriginRoundTrip(t *testing.T) {
	s, err := NewEphemeralSigner(65001)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := s.SignOrigin("203.0.113.53/32", 65000)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.Signature) != 64 {
		t.Fatalf("sig len %d", len(sig.Signature))
	}
	if len(sig.SKI) != 20 {
		t.Fatalf("ski len %d", len(sig.SKI))
	}
	if !s.VerifyOrigin(sig) {
		t.Fatal("self-verify failed")
	}
	if sig.TargetASN != 65000 || sig.OriginASN != 65001 {
		t.Fatalf("asn fields: %+v", sig)
	}
	if len(sig.BGPsecPathWire) < 30 {
		t.Fatalf("wire too short: %d", len(sig.BGPsecPathWire))
	}
}

func TestLoadSignerFromPEM(t *testing.T) {
	pem, ski, err := GenerateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "router.key")
	if err := os.WriteFile(path, []byte(pem), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSigner(path, ski, 64512)
	if err != nil {
		t.Fatal(err)
	}
	if s.SKIHex() != ski {
		t.Fatalf("ski mismatch %s vs %s", s.SKIHex(), ski)
	}
	sig, err := s.SignOrigin("192.0.2.1/32", 65000)
	if err != nil {
		t.Fatal(err)
	}
	if !s.VerifyOrigin(sig) {
		t.Fatal("verify failed")
	}
}

func TestRejectNonP256(t *testing.T) {
	// Empty PEM
	path := filepath.Join(t.TempDir(), "bad.key")
	_ = os.WriteFile(path, []byte("not-a-key"), 0o600)
	if _, err := LoadSigner(path, "", 65001); err == nil {
		t.Fatal("expected error")
	}
}
