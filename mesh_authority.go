package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0TrustCloud/secure_data_format"
	"github.com/0TrustCloud/secure_dns"
	"github.com/0TrustCloud/secure_registrar"
	"github.com/0TrustCloud/ultimate_db"
)

// meshAuthority is TunnelTug's built-in mesh nameservice: secure_dns zone storage
// plus secure_registrar ownership for private TLDs (default .tunnel).
type meshAuthority struct {
	mu            sync.RWMutex
	db            *ultimate_db.DB
	dns           *secure_dns.SecureDNS
	registrar     *secure_registrar.RegistrarEngine
	authoritative *secure_dns.AuthoritativeServer
	ownerPub      string
	ownerBytes    []byte
	edgeIP        string
	tld           string
	zone          string
	nsHost        string
	dnsListen     string
	dataDir       string
	closeFn       func()
}

var (
	globalMeshAuth   *meshAuthority
	globalMeshAuthMu sync.RWMutex
)

func setGlobalMeshAuthority(a *meshAuthority) {
	globalMeshAuthMu.Lock()
	globalMeshAuth = a
	globalMeshAuthMu.Unlock()
}

func getGlobalMeshAuthority() *meshAuthority {
	globalMeshAuthMu.RLock()
	defer globalMeshAuthMu.RUnlock()
	return globalMeshAuth
}

func meshAuthorityActive() bool {
	return getGlobalMeshAuthority() != nil
}

func meshPrivateName(hostID string) string {
	hostID = strings.ToLower(strings.TrimSpace(hostID))
	zone := strings.ToLower(strings.TrimSpace(*meshZone))
	if zone == "" {
		zone = "tunneltug.tunnel"
	}
	if hostID == "" {
		return zone
	}
	if hostID == zone || strings.HasSuffix(hostID, "."+zone) {
		return hostID
	}
	return hostID + "." + zone
}

func meshHostID() string {
	if v := strings.TrimSpace(*meshHost); v != "" {
		return strings.ToLower(v)
	}
	if isDirectRouting() {
		return "direct"
	}
	return strings.ToLower(strings.TrimSpace(*subdomain))
}

func resolveMeshDataDir() string {
	if v := strings.TrimSpace(*meshDataDir); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tunneltug", "mesh")
}

func resolveMeshEdgeIP() string {
	if v := strings.TrimSpace(*meshEdgeIP); v != "" {
		return v
	}
	if ip := detectOutboundIP(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

func detectOutboundIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
		return ip.String()
	}
	return ""
}

func openMeshDB(dataDir string) (*ultimate_db.DB, func(), error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, nil, err
	}
	dbPath := filepath.Join(dataDir, "mesh.db")
	walPath := dbPath + "_wal.log"
	device, err := ultimate_db.NewOSFileDevice(dbPath)
	if err != nil {
		return nil, nil, err
	}
	dm := ultimate_db.NewDiskManager(device)
	evictor := ultimate_db.NewLRUEvictionPolicy()
	metrics := ultimate_db.NewAtomicMetrics()
	bp := ultimate_db.NewBufferPool(dm, 1024, evictor, metrics)
	wal, err := ultimate_db.NewBatchingWAL(walPath)
	if err != nil {
		_ = device.Close()
		return nil, nil, err
	}
	db := ultimate_db.NewDB(bp, wal, metrics)
	if err := ultimate_db.PerformRecovery(db, walPath); err != nil {
		_ = db.Close()
		_ = device.Close()
		return nil, nil, err
	}
	return db, func() {
		_ = db.Close()
		// ultimate_db.Close does not close the block device; release the OS file on Windows.
		_ = device.Close()
	}, nil
}

func loadOrCreateMeshIdentity(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "identity.pub")
	if raw, err := os.ReadFile(path); err == nil {
		raw = []byte(strings.TrimSpace(string(raw)))
		if decoded, err := hex.DecodeString(string(raw)); err == nil && len(decoded) >= 16 {
			return decoded, nil
		}
		if len(raw) >= 16 {
			return raw, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(buf)), 0o600); err != nil {
		return nil, err
	}
	return buf, nil
}

func startMeshAuthority() (*meshAuthority, error) {
	if !meshActive() {
		return nil, nil
	}
	modeVal := strings.ToLower(strings.TrimSpace(*mode))
	if modeVal != "server" && modeVal != "lb" && modeVal != "orchestrator" {
		return nil, nil
	}

	dataDir := resolveMeshDataDir()
	db, closeDB, err := openMeshDB(dataDir)
	if err != nil {
		return nil, fmt.Errorf("mesh db: %w", err)
	}

	jwtKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		closeDB()
		return nil, fmt.Errorf("mesh sdf key: %w", err)
	}
	store := ultimate_db.NewBTreeKVStore(db)
	lockManager := ultimate_db.New2PLLockManager()
	sdf, err := secure_data_format.New(store, lockManager, "tunneltug-mesh", jwtKey)
	if err != nil {
		closeDB()
		return nil, fmt.Errorf("mesh sdf: %w", err)
	}

	ownerBytes, err := loadOrCreateMeshIdentity(dataDir)
	if err != nil {
		closeDB()
		return nil, fmt.Errorf("mesh identity: %w", err)
	}
	ownerPub := hex.EncodeToString(ownerBytes)

	// PeerRoute is nil: standalone authority (no external swarm required).
	dns := secure_dns.NewSecureDNS(nil, sdf, ownerBytes, db)
	registrar := secure_registrar.NewRegistrarEngine(db, dns)

	tld := strings.ToLower(strings.TrimSpace(*meshTLD))
	if tld == "" {
		tld = "tunnel"
	}
	zone := strings.ToLower(strings.TrimSpace(*meshZone))
	if zone == "" {
		zone = "tunneltug.tunnel"
	}
	nsHost := strings.ToLower(strings.TrimSpace(*meshNSHost))
	if nsHost == "" {
		nsHost = "ns." + zone
	}
	edgeIP := resolveMeshEdgeIP()
	dnsListen := strings.TrimSpace(*meshDNS)
	if dnsListen == "" {
		dnsListen = "127.0.0.1:5353"
	}

	auth := &meshAuthority{
		db:            db,
		dns:           dns,
		registrar:     registrar,
		authoritative: secure_dns.NewAuthoritativeServer(nsHost),
		ownerPub:      ownerPub,
		ownerBytes:    ownerBytes,
		edgeIP:        edgeIP,
		tld:           tld,
		zone:          zone,
		nsHost:        nsHost,
		dnsListen:     dnsListen,
		dataDir:       dataDir,
		closeFn:       closeDB,
	}

	if err := auth.bootstrapZones(); err != nil {
		auth.Close()
		return nil, err
	}
	if err := auth.startDNS(); err != nil {
		auth.Close()
		return nil, err
	}

	setGlobalMeshAuthority(auth)
	log.Printf("[mesh] authority online — zone=%s tld=.%s dns=%s edge=%s data=%s",
		zone, tld, dnsListen, edgeIP, dataDir)
	return auth, nil
}

func (a *meshAuthority) Close() {
	if a == nil {
		return
	}
	setGlobalMeshAuthority(nil)
	if a.authoritative != nil {
		a.authoritative.Shutdown()
	}
	if a.dns != nil {
		a.dns.Shutdown()
	}
	if a.closeFn != nil {
		a.closeFn()
		a.closeFn = nil
	}
}

func (a *meshAuthority) bootstrapZones() error {
	if err := a.registrar.EnsureTLD(a.tld, a.ownerPub); err != nil {
		return fmt.Errorf("ensure tld .%s: %w", a.tld, err)
	}
	_ = a.registrar.LockPlatformZone(a.tld)

	if err := a.registrar.EnsureRootDomain(a.zone, a.ownerPub); err != nil {
		return fmt.Errorf("ensure zone %s: %w", a.zone, err)
	}
	_ = a.registrar.LockPlatformZone(a.zone)

	// NS + apex glue for the private product zone.
	_ = a.dns.RegisterGlueRecord(a.tld, "NS", a.nsHost, 86400)
	_ = a.dns.RegisterGlueRecord(a.tld, "TXT", "TunnelTug private TLD — registrar: local mesh authority", 3600)
	_ = a.dns.ReplaceZoneType(a.nsHost, "A", a.edgeIP, 300)
	_ = a.dns.ReplaceZoneType(a.zone, "A", a.edgeIP, 300)
	_ = a.dns.RegisterGlueRecord(a.zone, "TXT", "public=tunneltug;role=mesh-edge", 3600)
	_ = a.dns.ReplaceZoneType("app."+a.zone, "A", a.edgeIP, 300)

	a.refreshAuthoritative()
	return nil
}

func (a *meshAuthority) startDNS() error {
	a.refreshAuthoritative()
	a.authoritative.IsPrivate = func(domain string) bool {
		return isMeshPrivateName(domain)
	}
	// No public recursion by default — keep authority private-mesh only.
	if err := a.authoritative.ServeUDP(a.dnsListen); err != nil {
		return fmt.Errorf("mesh dns udp %s: %w", a.dnsListen, err)
	}
	if err := a.authoritative.ServeTCP(a.dnsListen); err != nil {
		log.Printf("[mesh] authoritative TCP bind failed on %s: %v (UDP still active)", a.dnsListen, err)
	}
	// Also serve SecureDNS wire protocol on the same semantics via in-memory zone
	// (authoritative server is the public-facing path).
	return nil
}

func (a *meshAuthority) refreshAuthoritative() {
	if a == nil || a.dns == nil || a.authoritative == nil {
		return
	}
	snap, err := a.dns.BuildSnapshot(a.nsHost)
	if err != nil {
		return
	}
	a.authoritative.LoadSnapshot(snap)
}

func isMeshPrivateName(domain string) bool {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	tld := strings.ToLower(strings.TrimSpace(*meshTLD))
	if tld == "" {
		tld = "tunnel"
	}
	if domain == tld || strings.HasSuffix(domain, "."+tld) {
		return true
	}
	for _, suffix := range []string{".mesh", ".social", ".tunnel"} {
		if domain == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(domain, suffix) {
			return true
		}
	}
	return false
}

// PublishTunnel claims host_id under the product zone and publishes A/TXT records.
func (a *meshAuthority) PublishTunnel(hostID, edgeIP string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("mesh authority not running")
	}
	hostID = strings.ToLower(strings.TrimSpace(hostID))
	if hostID == "" {
		hostID = meshHostID()
	}
	if edgeIP == "" {
		edgeIP = a.edgeIP
	}
	name := meshPrivateName(hostID)

	a.mu.Lock()
	defer a.mu.Unlock()

	// Subdomain under product zone (e.g. myapp.tunneltug.tunnel).
	if name != a.zone {
		if err := a.registrar.RegisterSubdomain(name, a.ownerPub); err != nil {
			// Already held by us is fine; re-check ownership.
			if meta, getErr := a.registrar.GetOwnership(name); getErr != nil || meta.OwnerPub != a.ownerPub {
				// Idempotent re-publish: if already ours via Ensure path, continue on collision text.
				if !strings.Contains(err.Error(), "already held") && !strings.Contains(err.Error(), "namespace collision") {
					return "", fmt.Errorf("register %s: %w", name, err)
				}
			}
		}
	}

	if err := a.dns.ReplaceZoneType(name, "A", edgeIP, 60); err != nil {
		return "", fmt.Errorf("publish A %s: %w", name, err)
	}
	_ = a.dns.RegisterDomain(name, "TXT", "tunnel="+hostID+";edge="+edgeIP, 60)
	a.refreshAuthoritative()
	return name, nil
}

// UnpublishTunnel removes the A record for a disconnected tunnel (best-effort).
func (a *meshAuthority) UnpublishTunnel(hostID string) {
	if a == nil {
		return
	}
	name := meshPrivateName(hostID)
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.dns.DeleteZoneType(name, "A")
	a.refreshAuthoritative()
}

func (a *meshAuthority) Lookup(domain string) ([]secure_dns.DNSRecord, error) {
	if a == nil || a.dns == nil {
		return nil, fmt.Errorf("mesh authority not running")
	}
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	return a.dns.ResolveWire(domain, "A")
}

func (a *meshAuthority) Status() map[string]interface{} {
	if a == nil {
		return map[string]interface{}{"enabled": false}
	}
	records := 0
	if a.authoritative != nil {
		records = a.authoritative.RecordCount()
	}
	zones := 0
	if list, err := a.registrar.ListDomains(); err == nil {
		zones = len(list)
	}
	return map[string]interface{}{
		"enabled":     true,
		"tld":         a.tld,
		"zone":        a.zone,
		"ns_host":     a.nsHost,
		"dns_listen":  a.dnsListen,
		"edge_ip":     a.edgeIP,
		"records":     records,
		"zones":       zones,
		"owner_pub":   a.ownerPub[:minInt(16, len(a.ownerPub))],
		"data_dir":    a.dataDir,
		"protocols":   []string{"secure_dns", "secure_registrar", "authoritative_wire"},
	}
}
