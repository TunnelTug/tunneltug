// Package splitdns runs a secure_dns AuthoritativeServer with multihorizon split policy:
// configured suffixes → local zone; everything else → recursive.
package splitdns

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/0TrustCloud/secure_dns"

	"tunneltug/anycast/config"
	"tunneltug/anycast/zonepack"
)

// Server is an anycast edge split-horizon DNS front.
type Server struct {
	cfg      *config.Config
	auth     *secure_dns.AuthoritativeServer
	mu       sync.RWMutex
	suffixes []string
	records  int
	zonePack string
}

// New creates a split-horizon server from config (does not listen until Start).
func New(cfg *config.Config) *Server {
	host := cfg.NSHost
	if host == "" {
		host = "ns.anycast.local"
	}
	s := &Server{
		cfg:      cfg,
		auth:     secure_dns.NewAuthoritativeServer(host),
		suffixes: cfg.PrivateSuffixes(),
	}
	s.auth.IsPrivate = func(domain string) bool {
		return isPrivate(domain, s.privateSuffixes())
	}
	s.auth.Recurse = func(query []byte, domain string) []byte {
		resp, err := forwardWire(query, cfg.DNS.Recursive...)
		if err != nil || len(resp) == 0 {
			return nil
		}
		return resp
	}
	return s
}

func (s *Server) privateSuffixes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.suffixes...)
}

// BootstrapSeeds installs NS/A glue for configured TLDs/zone and optional zone_pack.
func (s *Server) BootstrapSeeds() {
	ip := strings.TrimSpace(s.cfg.DNS.AnycastIP)
	if ip == "" {
		ip = s.cfg.BGP.NextHop
	}
	if ip == "" {
		return
	}
	ttl := 60
	for _, tld := range s.cfg.TLDs {
		s.auth.UpsertRecord(tld, "NS", s.cfg.NSHost, 86400)
		s.auth.UpsertRecord(tld, "TXT", "anycast-updater private TLD — secure_dns split horizon", 3600)
	}
	if s.cfg.NSHost != "" {
		s.auth.UpsertRecord(s.cfg.NSHost, "A", ip, ttl)
	}
	if s.cfg.Zone != "" {
		s.auth.UpsertRecord(s.cfg.Zone, "A", ip, ttl)
		s.auth.UpsertRecord(s.cfg.Zone, "NS", s.cfg.NSHost, 86400)
		s.auth.UpsertRecord(s.cfg.Zone, "TXT", "public=tunneltug;role=anycast-edge", 3600)
	}

	if packPath := strings.TrimSpace(s.cfg.DNS.ZonePack); packPath != "" {
		s.loadZonePack(packPath, ip)
	}

	s.mu.Lock()
	s.records = s.auth.RecordCount()
	s.mu.Unlock()
	log.Printf("[dns] bootstrap glue tlds=%v zone=%s ns=%s ip=%s records=%d",
		s.cfg.TLDs, s.cfg.Zone, s.cfg.NSHost, ip, s.records)
}

func (s *Server) loadZonePack(path, ip string) {
	pack, err := zonepack.Load(path, ip)
	if err != nil {
		log.Printf("[dns] zone_pack load failed: %v", err)
		return
	}
	s.zonePack = pack.Path
	if len(pack.Snap.PrivateSuffixes) > 0 {
		s.mu.Lock()
		s.suffixes = mergeSuffixes(s.suffixes, pack.Snap.PrivateSuffixes)
		s.mu.Unlock()
	}
	for _, rec := range pack.Snap.Records {
		s.auth.UpsertRecord(rec.Domain, rec.Type, rec.Value, rec.TTL)
	}
	if s.cfg.NSHost != "" {
		s.auth.UpsertRecord(s.cfg.NSHost, "A", ip, 60)
	}
	log.Printf("[dns] zone_pack %s (%d records)", pack.Path, pack.Records)
}

// LoadSnapshot replaces the zone from a secure_dns sync payload and re-applies local glue.
func (s *Server) LoadSnapshot(snap secure_dns.ZoneSnapshot) {
	if snap.Host == "" {
		snap.Host = s.cfg.NSHost
	}
	s.auth.LoadSnapshot(snap)
	if len(snap.PrivateSuffixes) > 0 {
		s.mu.Lock()
		s.suffixes = normalizeSuffixes(snap.PrivateSuffixes)
		s.mu.Unlock()
	}
	ip := strings.TrimSpace(s.cfg.DNS.AnycastIP)
	if ip == "" {
		ip = s.cfg.BGP.NextHop
	}
	if ip != "" && s.cfg.NSHost != "" {
		s.auth.UpsertRecord(s.cfg.NSHost, "A", ip, 60)
	}
	for _, tld := range s.cfg.TLDs {
		s.auth.UpsertRecord(tld, "NS", s.cfg.NSHost, 86400)
	}
	// Re-apply local zone pack so remote sync does not drop file-seeded records.
	if packPath := strings.TrimSpace(s.cfg.DNS.ZonePack); packPath != "" && ip != "" {
		s.loadZonePack(packPath, ip)
	}
	s.mu.Lock()
	s.records = s.auth.RecordCount()
	s.mu.Unlock()
}

// Start binds UDP+TCP authoritative/split DNS.
func (s *Server) Start() error {
	if !s.cfg.DNS.Enabled {
		return nil
	}
	s.BootstrapSeeds()
	addr := s.cfg.DNS.Listen
	if err := s.auth.ServeUDP(addr); err != nil {
		return fmt.Errorf("dns udp %s: %w", addr, err)
	}
	if err := s.auth.ServeTCP(addr); err != nil {
		log.Printf("[dns] tcp bind failed on %s: %v (udp still active)", addr, err)
	}
	log.Printf("[dns] split-horizon listening on %s (suffixes → zone; else → %v)",
		addr, s.cfg.DNS.Recursive)
	return nil
}

// Shutdown stops the authoritative server.
func (s *Server) Shutdown() {
	if s.auth != nil {
		s.auth.Shutdown()
	}
}

// Status returns DNS edge state.
func (s *Server) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"enabled":          s.cfg.DNS.Enabled,
		"listen":           s.cfg.DNS.Listen,
		"host":             s.auth.Host(),
		"records":          s.auth.RecordCount(),
		"private_suffixes": append([]string{}, s.suffixes...),
		"recursive":        s.cfg.DNS.Recursive,
		"zone_pack":        s.zonePack,
	}
}

func mergeSuffixes(base, extra []string) []string {
	return normalizeSuffixes(append(append([]string{}, base...), extra...))
}

func isPrivate(domain string, suffixes []string) bool {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" {
		return false
	}
	for _, suffix := range suffixes {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix == "" {
			continue
		}
		if !strings.HasPrefix(suffix, ".") {
			suffix = "." + suffix
		}
		if domain == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(domain, suffix) {
			return true
		}
	}
	return false
}

func normalizeSuffixes(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if !strings.HasPrefix(s, ".") {
			s = "." + s
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func forwardWire(packet []byte, upstreams ...string) ([]byte, error) {
	var lastErr error
	for _, u := range upstreams {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.Contains(u, ":") {
			u += ":53"
		}
		resp, err := exchangeUDP(packet, u, 4*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no public upstream")
}

func exchangeUDP(packet []byte, upstream string, timeout time.Duration) ([]byte, error) {
	addr, err := net.ResolveUDPAddr("udp", upstream)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n < 12 {
		return nil, fmt.Errorf("short upstream response")
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	flags := binary.BigEndian.Uint16(out[2:4])
	if flags&0x8000 == 0 {
		return nil, fmt.Errorf("upstream response missing QR")
	}
	return out, nil
}
