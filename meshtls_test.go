package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsMeshTLSHostname(t *testing.T) {
	prevZ, prevT, prevD, prevS := *meshZone, *meshTLD, *domain, *subalt
	t.Cleanup(func() {
		*meshZone, *meshTLD, *domain, *subalt = prevZ, prevT, prevD, prevS
	})
	*meshZone = "mitigation.mesh"
	*meshTLD = "mesh"
	*domain = "example.com"
	*subalt = "*.example.com,app.example.com"

	if !isMeshTLSHostname("platform.mitigation.mesh") {
		t.Fatal("expected mesh zone host")
	}
	if !isMeshTLSHostname("platform.mitigation.mesh:4073") {
		t.Fatal("expected mesh host with port")
	}
	if !isMeshTLSHostname("www.example.com") {
		t.Fatal("expected -domain suffix")
	}
	if !isMeshTLSHostname("app.example.com") {
		t.Fatal("expected -subalt exact")
	}
	if !isMeshTLSHostname("foo.example.com") {
		t.Fatal("expected -subalt wildcard")
	}
	if isMeshTLSHostname("127.0.0.1") || isMeshTLSHostname("localhost") {
		t.Fatal("loopback must not be meshTLS")
	}
}

func TestMeshTLSProvisionNamesIncludesDomainSubalt(t *testing.T) {
	prevZ, prevD, prevS := *meshZone, *domain, *subalt
	t.Cleanup(func() { *meshZone, *domain, *subalt = prevZ, prevD, prevS })
	*meshZone = "mitigation.mesh"
	*domain = "evilrbi.com"
	*subalt = "*.evilrbi.com,www.evilrbi.com"
	names := meshTLSProvisionNames()
	joined := strings.Join(names, ",")
	for _, want := range []string{"mitigation.mesh", "*.mitigation.mesh", "stack.mitigation.mesh", "evilrbi.com", "*.evilrbi.com", "www.evilrbi.com"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, names)
		}
	}
}

func TestMeshTLSLeafFromDiskStore(t *testing.T) {
	// Simulate platform-issued leaf on disk (gonode certs/active layout) — no local CA invent.
	prevTLS, prevDir := *meshTLS, *meshTLSDataDir
	t.Cleanup(func() {
		*meshTLS, *meshTLSDataDir = prevTLS, prevDir
		meshTLSMu.Lock()
		meshTLSRoot = nil
		meshTLSRootPEM = nil
		meshTLSPool = nil
		meshTLSLeaves = map[string]*tls.Certificate{}
		meshTLSMu.Unlock()
	})
	dir := t.TempDir()
	*meshTLS = true
	*meshTLSDataDir = dir

	// Minimal self-signed only as test fixture (stands in for Mesh CA leaf on disk).
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "app.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"app.example.com", "*.example.com"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	base := filepath.Join(dir, "app_example_com")
	if err := os.WriteFile(base+".crt", certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".key", keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	// Root for client trust pool
	_ = os.WriteFile(filepath.Join(dir, "mesh-root.crt"), certPEM, 0o644)

	reloadMeshTLSLeavesFromDisk(dir)
	if err := loadMeshTLSRoot(dir); err != nil {
		t.Fatal(err)
	}
	leaf, err := ensureMeshTLSLeaf("app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if leaf == nil || len(leaf.Certificate) == 0 {
		t.Fatal("empty leaf")
	}
	// Wildcard SNI via DNSNames on same cert
	leaf2, err := ensureMeshTLSLeaf("other.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if leaf2 == nil {
		t.Fatal("wildcard leaf")
	}
	cfg := meshTLSClientConfig("app.example.com")
	if cfg.ServerName != "app.example.com" || cfg.RootCAs == nil {
		t.Fatalf("client cfg %+v", cfg)
	}
	_ = tls.Certificate{}
}
