package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// meshTLS is the FQDN TLS plane for WebAuthn RP ID, DBSC, and HTTP/3 SNI.
//
// Normative path (same as gonode / 0TrustOS microkernel ProvisionMesh):
//
//	0trust.cloud Mesh CA
//	  → nameservice provision (A + EnsureMeshCert for the FQDN)
//	  → GET /api/v1/pki/mesh-root
//	  → GET /api/v1/pki/certs/active (X-0Trust-Sync-Token)
//	  → serve leaf by SNI
//
// TunnelTug does NOT invent a mesh CA. Leaves are issued by platform PKI when
// the FQDN is published (or already present in active certs). Product pods run
// gonode which performs the same provision for OTRUST_MESH_DOMAIN.
//
// Production mesh uses real domains (and wildcards / -subalt). Private .mesh
// zones are valid FQDNs on that plane too — still Mesh CA certs, never ACME.

var (
	meshTLSMu      sync.RWMutex
	meshTLSRoot    *x509.Certificate
	meshTLSRootPEM []byte
	meshTLSPool    *x509.CertPool
	// Leaves from platform sync / disk — keyed by DNS name (incl. wildcards).
	meshTLSLeaves = map[string]*tls.Certificate{}
)

func meshTLSDir() string {
	if v := strings.TrimSpace(*meshTLSDataDir); v != "" {
		return v
	}
	return filepath.Join(resolveMeshDataDir(), "meshtls")
}

func meshTLSActive() bool {
	return *meshTLS || meshActive()
}

func meshTLSPlatformBase() string {
	if meshPlatform != nil {
		if v := strings.TrimRight(strings.TrimSpace(*meshPlatform), "/"); v != "" {
			return v
		}
	}
	return "https://0trust.cloud"
}

func meshTLSSyncToken() string {
	if authToken != nil {
		if t := strings.TrimSpace(*authToken); t != "" {
			return t
		}
	}
	for _, k := range []string{"OTRUST_SYNC_TOKEN", "OTRUST_TUNNEL_TOKEN", "TUNNELTUG_TOKEN", "TUNNEL_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// isMeshTLSHostname is true for FQDNs that use the Mesh CA plane (WebAuthn/DBSC/SNI).
func isMeshTLSHostname(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return false
	}
	// Configured mesh zone / TLD.
	zone := strings.ToLower(strings.TrimSpace(*meshZone))
	tld := strings.ToLower(strings.TrimSpace(*meshTLD))
	if zone != "" && (host == zone || strings.HasSuffix(host, "."+zone)) {
		return true
	}
	if tld != "" && strings.HasSuffix(host, "."+tld) {
		return true
	}
	// Primary -domain and -subalt (prod mesh FQDNs).
	if d := strings.ToLower(strings.TrimSpace(*domain)); d != "" {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	for _, a := range strings.Split(*subalt, ",") {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "*.") {
			suf := strings.TrimPrefix(a, "*")
			if strings.HasSuffix(host, suf) {
				return true
			}
			continue
		}
		if host == a {
			return true
		}
	}
	// Common mesh product suffixes.
	for _, s := range []string{".mesh", ".tunnel", ".social"} {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// meshTLSProvisionNames returns FQDNs that must have Mesh CA leaves for WebAuthn/DBSC.
func meshTLSProvisionNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		name = strings.TrimPrefix(strings.TrimPrefix(name, "https://"), "http://")
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		if i := strings.IndexByte(name, ':'); i >= 0 {
			// keep wildcards like *.example.com — only strip port if present after host
			if !strings.HasPrefix(name, "*.") {
				if h, _, err := net.SplitHostPort(name); err == nil {
					name = h
				}
			}
		}
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	zone := strings.ToLower(strings.TrimSpace(*meshZone))
	if zone != "" {
		add(zone)
		add("*." + zone)
		add("stack." + zone)
		add("ns." + zone)
	}
	if d := strings.TrimSpace(*domain); d != "" {
		add(d)
		add("*." + d)
		add("www." + d)
		add("stack." + d)
	}
	for _, a := range strings.Split(*subalt, ",") {
		add(a)
	}
	// Stack operator face always.
	add(stackOperatorMeshHost())
	return out
}

// ensureMeshTLS loads platform Mesh CA trust + active leaves (gonode path).
// Never invents a local mesh root.
func ensureMeshTLS() error {
	if !meshTLSActive() {
		return nil
	}
	dir := meshTLSDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cloud := meshTLSPlatformBase()
	tok := meshTLSSyncToken()

	// 1) Mesh root trust anchor (public).
	if err := pullMeshRootFromPlatform(ctx, cloud, dir); err != nil {
		// Keep existing root on disk if present.
		if _, st := os.Stat(filepath.Join(dir, "mesh-root.crt")); st != nil {
			log.Printf("[meshtls] mesh-root pull failed: %v (need platform Mesh CA)", err)
		} else {
			log.Printf("[meshtls] mesh-root pull: %v — using cached root", err)
		}
	}

	// 2) Publish FQDNs on nameservice → platform EnsureMeshCert issues leaves.
	//    Same trigger gonode uses after agent provision.
	edge := resolveMeshEdgeIP()
	if edge == "" {
		edge = "127.0.0.1"
	}
	names := meshTLSProvisionNames()
	if tok != "" {
		for _, name := range names {
			// Skip pure wildcards for DNS A — publish apex + stack + bare names.
			if strings.HasPrefix(name, "*.") {
				continue
			}
			if err := publishMeshDNSForTLS(ctx, cloud, tok, name, edge); err != nil {
				log.Printf("[meshtls] nameservice provision %s: %v", name, err)
			} else {
				log.Printf("[meshtls] nameservice provision %s A %s (triggers Mesh CA leaf)", name, edge)
			}
		}
	} else {
		log.Printf("[meshtls] no sync/tunnel token — cannot publish FQDNs or pull leaves (set -token / OTRUST_SYNC_TOKEN)")
	}

	// 3) Pull active meshTLS leaves from platform (gonode pullMeshTLSLeaf).
	if tok != "" {
		if err := pullMeshTLSActiveCerts(ctx, cloud, tok, dir); err != nil {
			log.Printf("[meshtls] certs/active: %v", err)
		}
	}

	// 4) Load root + any on-disk leaves into memory for SNI.
	if err := loadMeshTLSRoot(dir); err != nil {
		log.Printf("[meshtls] load root: %v", err)
	}
	reloadMeshTLSLeavesFromDisk(dir)

	meshTLSMu.RLock()
	nLeaf := len(meshTLSLeaves)
	haveRoot := meshTLSPool != nil
	meshTLSMu.RUnlock()
	log.Printf("[meshtls] ready dir=%s root=%v leaves=%d platform=%s (Mesh CA via gonode path; no local CA invent)",
		dir, haveRoot, nLeaf, cloud)
	if nLeaf == 0 {
		log.Printf("[meshtls] WARNING: no Mesh CA leaves yet — WebAuthn/DBSC FQDN TLS will fail until nameservice+PKI issue for: %s",
			strings.Join(names, ", "))
	}
	return nil
}

func pullMeshRootFromPlatform(ctx context.Context, cloud, dir string) error {
	url := strings.TrimRight(cloud, "/") + "/api/v1/pki/mesh-root"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !bytes.Contains(raw, []byte("BEGIN CERTIFICATE")) {
		return fmt.Errorf("not a PEM certificate")
	}
	_ = os.WriteFile(filepath.Join(dir, "mesh-root.pem"), raw, 0o644)
	return os.WriteFile(filepath.Join(dir, "mesh-root.crt"), raw, 0o644)
}

// publishMeshDNSForTLS posts A record; platform nameservice calls EnsureMeshCert.
func publishMeshDNSForTLS(ctx context.Context, cloud, token, domain, edgeIP string) error {
	body, _ := json.Marshal(map[string]any{
		"domain": domain,
		"type":   "A",
		"value":  edgeIP,
		"ttl":    60,
	})
	url := strings.TrimRight(cloud, "/") + "/api/v1/nameservice/provision/record"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-0Trust-Sync-Token", token)
	req.Header.Set("X-Tunnel-Token", token)
	req.Header.Set("X-0Trust-Tunnel-Token", token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(strings.TrimSpace(string(raw)), 200))
	}
	return nil
}

type meshTLSCertJSON struct {
	ID       string `json:"id"`
	Domain   string `json:"domain"`
	CertPEM  string `json:"cert_pem"`
	ChainPEM string `json:"chain_pem"`
	KeyPEM   string `json:"key_pem"`
	Status   string `json:"status"`
}

func pullMeshTLSActiveCerts(ctx context.Context, cloud, token, dir string) error {
	url := strings.TrimRight(cloud, "/") + "/api/v1/pki/certs/active"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-0Trust-Sync-Token", token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(strings.TrimSpace(string(raw)), 160))
	}
	var payload struct {
		Certs    []meshTLSCertJSON `json:"certs"`
		MeshRoot string            `json:"mesh_root"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if payload.MeshRoot != "" && strings.Contains(payload.MeshRoot, "BEGIN CERTIFICATE") {
		_ = os.WriteFile(filepath.Join(dir, "mesh-root.pem"), []byte(payload.MeshRoot), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "mesh-root.crt"), []byte(payload.MeshRoot), 0o644)
	}
	n := 0
	for _, cert := range payload.Certs {
		if cert.Domain == "" || cert.CertPEM == "" || cert.Status == "revoked" {
			continue
		}
		// Filename style matches gonode/services: dots → underscores.
		base := filepath.Join(dir, strings.ReplaceAll(strings.ReplaceAll(cert.Domain, "*", "_wild_"), ".", "_"))
		crt := strings.TrimSpace(cert.CertPEM) + "\n"
		if chain := strings.TrimSpace(cert.ChainPEM); chain != "" {
			crt += chain + "\n"
		}
		if err := os.WriteFile(base+".crt", []byte(crt), 0o644); err != nil {
			continue
		}
		if cert.KeyPEM != "" {
			_ = os.WriteFile(base+".key", []byte(cert.KeyPEM), 0o600)
		}
		n++
	}
	log.Printf("[meshtls] synced %d Mesh CA leaves from %s/api/v1/pki/certs/active", n, cloud)
	return nil
}

func loadMeshTLSRoot(dir string) error {
	certPEM, err := os.ReadFile(filepath.Join(dir, "mesh-root.crt"))
	if err != nil {
		// try .pem
		certPEM, err = os.ReadFile(filepath.Join(dir, "mesh-root.pem"))
		if err != nil {
			return err
		}
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("invalid mesh-root.crt")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	meshTLSMu.Lock()
	meshTLSRoot = cert
	meshTLSRootPEM = certPEM
	meshTLSPool = pool
	meshTLSMu.Unlock()
	return nil
}

func reloadMeshTLSLeavesFromDisk(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	loaded := map[string]*tls.Certificate{}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".crt") {
			continue
		}
		if ent.Name() == "mesh-root.crt" {
			continue
		}
		base := strings.TrimSuffix(ent.Name(), ".crt")
		certPath := filepath.Join(dir, ent.Name())
		keyPath := filepath.Join(dir, base+".key")
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			continue
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			// Leaf without key cannot be served as server cert.
			continue
		}
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			log.Printf("[meshtls] skip %s: %v", base, err)
			continue
		}
		// Map DNSNames from leaf.
		if len(tlsCert.Certificate) > 0 {
			if leaf, err := x509.ParseCertificate(tlsCert.Certificate[0]); err == nil {
				for _, name := range leaf.DNSNames {
					loaded[strings.ToLower(name)] = &tlsCert
				}
				if leaf.Subject.CommonName != "" {
					loaded[strings.ToLower(leaf.Subject.CommonName)] = &tlsCert
				}
			}
		}
		// Filename domain: underscores → dots, _wild_ → *
		domain := strings.ReplaceAll(base, "_wild_", "*")
		domain = strings.ReplaceAll(domain, "_", ".")
		loaded[strings.ToLower(domain)] = &tlsCert
	}
	meshTLSMu.Lock()
	for k, v := range loaded {
		meshTLSLeaves[k] = v
	}
	meshTLSMu.Unlock()
}

// ensureMeshTLSLeaf returns a Mesh CA leaf for domain (from platform sync store only).
func ensureMeshTLSLeaf(domain string) (*tls.Certificate, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, fmt.Errorf("domain required")
	}
	meshTLSMu.RLock()
	if leaf, ok := meshTLSLeaves[domain]; ok {
		meshTLSMu.RUnlock()
		return leaf, nil
	}
	// Wildcard match.
	for pattern, cert := range meshTLSLeaves {
		if strings.HasPrefix(pattern, "*.") {
			suf := strings.TrimPrefix(pattern, "*")
			if strings.HasSuffix(domain, suf) && domain != strings.TrimPrefix(suf, ".") {
				meshTLSMu.RUnlock()
				return cert, nil
			}
		}
	}
	meshTLSMu.RUnlock()

	// Refresh store once, then retry (cert may have been issued after nameservice).
	dir := meshTLSDir()
	tok := meshTLSSyncToken()
	if tok != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = pullMeshTLSActiveCerts(ctx, meshTLSPlatformBase(), tok, dir)
		cancel()
		reloadMeshTLSLeavesFromDisk(dir)
		meshTLSMu.RLock()
		if leaf, ok := meshTLSLeaves[domain]; ok {
			meshTLSMu.RUnlock()
			return leaf, nil
		}
		for pattern, cert := range meshTLSLeaves {
			if strings.HasPrefix(pattern, "*.") {
				suf := strings.TrimPrefix(pattern, "*")
				if strings.HasSuffix(domain, suf) && domain != strings.TrimPrefix(suf, ".") {
					meshTLSMu.RUnlock()
					return cert, nil
				}
			}
		}
		meshTLSMu.RUnlock()
	}
	return nil, fmt.Errorf("no Mesh CA leaf for %s — gonode/platform must provision FQDN (nameservice + certs/active)", domain)
}

// meshTLSClientConfig trusts the platform Mesh root for mesh FQDN dials.
func meshTLSClientConfig(serverName string) *tls.Config {
	serverName = strings.ToLower(strings.TrimSpace(serverName))
	if h, _, err := net.SplitHostPort(serverName); err == nil {
		serverName = h
	}
	meshTLSMu.RLock()
	pool := meshTLSPool
	meshTLSMu.RUnlock()
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		NextProtos: []string{"h3", "h2", "http/1.1"},
	}
	if pool != nil {
		cfg.RootCAs = pool
		cfg.InsecureSkipVerify = false
	} else if *insecure {
		cfg.InsecureSkipVerify = true
	}
	return cfg
}

// meshTLSServerConfig serves Mesh CA leaves by SNI for WebAuthn/DBSC FQDNs.
func meshTLSServerConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := strings.ToLower(strings.TrimSpace(chi.ServerName))
			if name == "" {
				return nil, fmt.Errorf("meshtls: no SNI")
			}
			if !isMeshTLSHostname(name) {
				return nil, fmt.Errorf("meshtls: not a meshTLS FQDN %s", name)
			}
			if leaf, err := ensureMeshTLSLeaf(name); err == nil {
				return leaf, nil
			}
			// Wildcard under zone / domain.
			zone := strings.ToLower(strings.TrimSpace(*meshZone))
			if zone != "" && strings.HasSuffix(name, "."+zone) {
				if leaf, err := ensureMeshTLSLeaf("*." + zone); err == nil {
					return leaf, nil
				}
			}
			if d := strings.ToLower(strings.TrimSpace(*domain)); d != "" && strings.HasSuffix(name, "."+d) {
				if leaf, err := ensureMeshTLSLeaf("*." + d); err == nil {
					return leaf, nil
				}
			}
			return ensureMeshTLSLeaf(name)
		},
	}
}

func tlsConfigForDial(host string) *tls.Config {
	if isMeshTLSHostname(host) && meshTLSActive() {
		return meshTLSClientConfig(host)
	}
	return clientTLSConfig()
}

func httpClientForURL(rawURL string, timeout time.Duration) *http.Client {
	host := ""
	if u, err := parseURLHost(rawURL); err == nil {
		host = u
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfigForDial(host),
		Proxy:           http.ProxyFromEnvironment,
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func parseURLHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty url")
	}
	// Minimal parse without importing net/url cycle issues — use full url.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	// defer to stdlib via simple split
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		rest = raw[i+3:]
	}
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	if h, _, err := net.SplitHostPort(rest); err == nil {
		return h, nil
	}
	return rest, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

