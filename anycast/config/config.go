// Package config loads the anycast updater YAML configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"tunneltug/anycast/origin"
)

// Config is the root configuration for the BGP anycast updater.
type Config struct {
	NodeID string         `yaml:"node_id"`
	Listen string         `yaml:"listen"`
	TLDs   []string       `yaml:"tlds"`
	Zone   string         `yaml:"zone"`
	NSHost string         `yaml:"ns_host"`
	DNS    DNSConfig      `yaml:"dns"`
	Sync   SyncConfig     `yaml:"sync"`
	Health HealthConfig   `yaml:"health"`
	BGP    BGPConfig      `yaml:"bgp"`
	Origin origin.Config  `yaml:"origin"`
}

type DNSConfig struct {
	Enabled         bool     `yaml:"enabled"`
	Listen          string   `yaml:"listen"`
	Recursive       []string `yaml:"recursive"`
	AnycastIP       string   `yaml:"anycast_ip"`
	PrivateSuffixes []string `yaml:"private_suffixes"`
	// ZonePack is a local secure_dns.ZoneSnapshot JSON file or directory of *.json.
	// Supports {{VIP}} / {{ANYCAST_IP}} placeholders. Applied at bootstrap.
	ZonePack string `yaml:"zone_pack"`
}

type SyncConfig struct {
	Enabled  bool          `yaml:"enabled"`
	URL      string        `yaml:"url"`
	Token    string        `yaml:"token"`
	Interval time.Duration `yaml:"interval"`
	MeshDNS  string        `yaml:"mesh_dns"`
}

type HealthConfig struct {
	Interval         time.Duration `yaml:"interval"`
	FailThreshold    int           `yaml:"fail_threshold"`
	RecoverThreshold int           `yaml:"recover_threshold"`
	Timeout          time.Duration `yaml:"timeout"`
	DNSProbe         DNSProbe      `yaml:"dns_probe"`
	HTTPProbes       []HTTPProbe   `yaml:"http_probes"`
	TCPProbes        []string      `yaml:"tcp_probes"`
}

type DNSProbe struct {
	Enabled     bool     `yaml:"enabled"`
	Target      string   `yaml:"target"`
	Names       []string `yaml:"names"`
	ExpectTypes []string `yaml:"expect_types"`
}

type HTTPProbe struct {
	URL          string `yaml:"url"`
	ExpectStatus int    `yaml:"expect_status"`
}

type BGPConfig struct {
	Backend     string   `yaml:"backend"` // log | exabgp | bird | file
	LocalASN    uint32   `yaml:"local_asn"`
	PeerASN     uint32   `yaml:"peer_asn"`
	// IBGP: when true and peer_asn is 0, peer_asn defaults to local_asn.
	IBGP        bool     `yaml:"ibgp"`
	NextHop     string   `yaml:"next_hop"`
	Prefixes    []string `yaml:"prefixes"`
	Communities []string `yaml:"communities"`
	ExaBGP      ExaBGPConfig `yaml:"exabgp"`
	Bird        BirdConfig   `yaml:"bird"`
	File        FileConfig   `yaml:"file"`
	// Security: RPKI ROV + in-process BGPsec origin signing (not ACME/TLS keys).
	Security SecurityConfig `yaml:"security"`
}

type ExaBGPConfig struct {
	CommandPath string `yaml:"command_path"`
}

type BirdConfig struct {
	IncludePath string `yaml:"include_path"`
	Birdc       string `yaml:"birdc"`
	Protocol    string `yaml:"protocol"`
}

type FileConfig struct {
	Path string `yaml:"path"`
}

// SecurityConfig gates announces with ROV and signs with BGPsec router keys.
type SecurityConfig struct {
	// FailClosed (default true when bgpsec or rov require_* set) withdraws if checks fail.
	FailClosed *bool `yaml:"fail_closed"`
	ROV        ROVConfig    `yaml:"rov"`
	BGPsec     BGPsecConfig `yaml:"bgpsec"`
}

// ROVConfig is RPKI Route Origin Validation policy (RFC 6811).
type ROVConfig struct {
	Enabled      bool     `yaml:"enabled"`
	RequireValid bool     `yaml:"require_valid"` // NotFound → reject
	AllowPrivate bool     `yaml:"allow_private"` // DOC/RFC1918 + private ASN lab paths
	ROAFile      string   `yaml:"roa_file"`      // JSON ROA list
	ROAs         []ROAEntry `yaml:"roas"`
}

// ROAEntry is one Route Origin Authorization.
type ROAEntry struct {
	Prefix    string `yaml:"prefix" json:"prefix"`
	ASN       uint32 `yaml:"asn" json:"asn"`
	MaxLength int    `yaml:"max_length" json:"max_length"`
}

// BGPsecConfig enables in-tool origin signing (RFC 8205/8208 suite 1).
// Private key is an RPKI BGPsec router key (ECDSA P-256) — not the ACME TLS key.
type BGPsecConfig struct {
	Enabled     bool   `yaml:"enabled"`
	RequireSign bool   `yaml:"require_sign"` // fail announce if signing fails
	PrivateKey  string `yaml:"private_key"`  // PEM path
	SKI         string `yaml:"ski"`          // optional 20-octet hex; derived if empty
	// TargetASN is the eBGP peer AS bound into each signature (0 → bgp.peer_asn).
	TargetASN uint32 `yaml:"target_asn"`
	// SignatureFile optionally writes per-prefix signature export (JSON lines).
	SignatureFile string `yaml:"signature_file"`
}

// Load reads and validates a YAML config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.NodeID == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "anycast-node"
		}
		c.NodeID = host
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:9099"
	}
	if len(c.TLDs) == 0 {
		c.TLDs = []string{"tunnel"}
	}
	for i, t := range c.TLDs {
		c.TLDs[i] = normalizeTLD(t)
	}
	if c.Zone == "" && len(c.TLDs) > 0 {
		c.Zone = "tunneltug." + c.TLDs[0]
	}
	c.Zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Zone), "."))
	if c.NSHost == "" && c.Zone != "" {
		c.NSHost = "ns." + c.Zone
	}
	c.NSHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.NSHost), "."))

	if c.DNS.Listen == "" {
		c.DNS.Listen = "127.0.0.1:5353"
	}
	if len(c.DNS.Recursive) == 0 {
		c.DNS.Recursive = []string{"1.1.1.1:53", "8.8.8.8:53"}
	}
	if len(c.DNS.PrivateSuffixes) == 0 {
		for _, t := range c.TLDs {
			c.DNS.PrivateSuffixes = append(c.DNS.PrivateSuffixes, "."+t)
		}
		for _, s := range []string{".mesh", ".social", ".tunnel"} {
			if !contains(c.DNS.PrivateSuffixes, s) {
				c.DNS.PrivateSuffixes = append(c.DNS.PrivateSuffixes, s)
			}
		}
	} else {
		for i, s := range c.DNS.PrivateSuffixes {
			c.DNS.PrivateSuffixes[i] = normalizeSuffix(s)
		}
	}
	if c.DNS.AnycastIP == "" && c.BGP.NextHop != "" {
		c.DNS.AnycastIP = c.BGP.NextHop
	}

	if c.Origin.Enabled && strings.TrimSpace(c.Origin.Listen) == "" {
		if ip := net.ParseIP(c.DNS.AnycastIP); ip != nil && ip.IsLoopback() {
			c.Origin.Listen = net.JoinHostPort(c.DNS.AnycastIP, "8080")
		} else {
			c.Origin.Listen = "127.0.0.1:8080"
		}
	}

	if c.Sync.Interval <= 0 {
		c.Sync.Interval = 30 * time.Second
	}

	if c.Health.Interval <= 0 {
		c.Health.Interval = 5 * time.Second
	}
	if c.Health.FailThreshold <= 0 {
		c.Health.FailThreshold = 3
	}
	if c.Health.RecoverThreshold <= 0 {
		c.Health.RecoverThreshold = 2
	}
	if c.Health.Timeout <= 0 {
		c.Health.Timeout = 2 * time.Second
	}
	if c.Health.DNSProbe.Enabled && c.Health.DNSProbe.Target == "" {
		// Prefer loopback of the local DNS listen port for self-checks.
		host, port, err := net.SplitHostPort(c.DNS.Listen)
		if err == nil {
			if host == "0.0.0.0" || host == "::" || host == "" {
				host = "127.0.0.1"
			}
			c.Health.DNSProbe.Target = net.JoinHostPort(host, port)
		} else {
			c.Health.DNSProbe.Target = "127.0.0.1:53"
		}
	}
	if c.Health.DNSProbe.Enabled && len(c.Health.DNSProbe.Names) == 0 {
		c.Health.DNSProbe.Names = append([]string{}, c.TLDs...)
		if c.NSHost != "" {
			c.Health.DNSProbe.Names = append(c.Health.DNSProbe.Names, c.NSHost)
		}
		if c.Zone != "" {
			c.Health.DNSProbe.Names = append(c.Health.DNSProbe.Names, c.Zone)
		}
	}
	if len(c.Health.DNSProbe.ExpectTypes) == 0 {
		c.Health.DNSProbe.ExpectTypes = []string{"A", "NS"}
	}

	if c.BGP.Backend == "" {
		c.BGP.Backend = "log"
	}
	c.BGP.Backend = strings.ToLower(strings.TrimSpace(c.BGP.Backend))
	if c.BGP.LocalASN == 0 {
		c.BGP.LocalASN = 65001
	}
	if c.BGP.IBGP && c.BGP.PeerASN == 0 {
		c.BGP.PeerASN = c.BGP.LocalASN
	}
	if c.BGP.NextHop == "" && c.DNS.AnycastIP != "" {
		c.BGP.NextHop = c.DNS.AnycastIP
	}
	if c.BGP.ExaBGP.CommandPath == "" {
		c.BGP.ExaBGP.CommandPath = "exabgp.cmd"
	}
	if c.BGP.Bird.IncludePath == "" {
		c.BGP.Bird.IncludePath = "/etc/bird/anycast-routes.conf"
	}
	if c.BGP.Bird.Birdc == "" {
		c.BGP.Bird.Birdc = "birdc"
	}
	if c.BGP.Bird.Protocol == "" {
		c.BGP.Bird.Protocol = "anycast4"
	}
	if c.BGP.File.Path == "" {
		c.BGP.File.Path = "state/announced.routes"
	}

	// Security defaults: lab-friendly ROV for private ASN/DOC space when enabled empty.
	if c.BGP.Security.FailClosed == nil {
		// Default fail-closed when either hard gate is on.
		fc := c.BGP.Security.BGPsec.RequireSign || c.BGP.Security.ROV.RequireValid ||
			c.BGP.Security.BGPsec.Enabled || c.BGP.Security.ROV.Enabled
		c.BGP.Security.FailClosed = &fc
	}
	if c.BGP.Security.BGPsec.Enabled && c.BGP.Security.BGPsec.TargetASN == 0 {
		c.BGP.Security.BGPsec.TargetASN = c.BGP.PeerASN
	}
	// Private ASN lab: allow_private defaults on when ROV enabled and no ROAs.
	if c.BGP.Security.ROV.Enabled && !c.BGP.Security.ROV.RequireValid &&
		len(c.BGP.Security.ROV.ROAs) == 0 && c.BGP.Security.ROV.ROAFile == "" {
		// enable allow_private for private ASNs unless explicitly false in YAML is hard;
		// we only set true when local_asn is private and allow_private was left default false
		// with no ROAs — operators with public ASN should set require_valid + roas.
		if isPrivateASN(c.BGP.LocalASN) {
			c.BGP.Security.ROV.AllowPrivate = true
		}
	}
}

func isPrivateASN(asn uint32) bool {
	if asn >= 64512 && asn <= 65534 {
		return true
	}
	if asn >= 4200000000 && asn <= 4294967294 {
		return true
	}
	return false
}

// Validate checks required fields.
func (c *Config) Validate() error {
	if len(c.TLDs) == 0 {
		return fmt.Errorf("tlds: at least one private TLD is required")
	}
	for _, t := range c.TLDs {
		if t == "" || strings.Contains(t, ".") {
			return fmt.Errorf("tlds: %q must be a single label", t)
		}
	}
	if len(c.BGP.Prefixes) == 0 {
		return fmt.Errorf("bgp.prefixes: at least one anycast prefix is required")
	}
	for _, p := range c.BGP.Prefixes {
		if _, _, err := net.ParseCIDR(p); err != nil {
			return fmt.Errorf("bgp.prefixes: invalid CIDR %q: %w", p, err)
		}
	}
	if c.BGP.NextHop == "" {
		return fmt.Errorf("bgp.next_hop (or dns.anycast_ip) is required")
	}
	if ip := net.ParseIP(c.BGP.NextHop); ip == nil {
		return fmt.Errorf("bgp.next_hop: invalid IP %q", c.BGP.NextHop)
	}
	switch c.BGP.Backend {
	case "log", "exabgp", "bird", "file":
	default:
		return fmt.Errorf("bgp.backend: unknown %q (log|exabgp|bird|file)", c.BGP.Backend)
	}
	if c.DNS.Enabled && c.DNS.AnycastIP != "" {
		if ip := net.ParseIP(c.DNS.AnycastIP); ip == nil {
			return fmt.Errorf("dns.anycast_ip: invalid IP %q", c.DNS.AnycastIP)
		}
	}
	if err := c.validateSecurity(); err != nil {
		return err
	}
	if pack := strings.TrimSpace(c.DNS.ZonePack); pack != "" {
		if _, err := os.Stat(pack); err != nil {
			return fmt.Errorf("dns.zone_pack: %w", err)
		}
	}
	return nil
}

func (c *Config) validateSecurity() error {
	sec := c.BGP.Security
	if sec.BGPsec.Enabled {
		if strings.TrimSpace(sec.BGPsec.PrivateKey) == "" {
			return fmt.Errorf("bgp.security.bgpsec.private_key is required when bgpsec.enabled")
		}
		if _, err := os.Stat(sec.BGPsec.PrivateKey); err != nil {
			return fmt.Errorf("bgp.security.bgpsec.private_key: %w", err)
		}
		target := sec.BGPsec.TargetASN
		if target == 0 {
			target = c.BGP.PeerASN
		}
		if target == 0 {
			return fmt.Errorf("bgp.security.bgpsec: target_asn or bgp.peer_asn required (BGPsec binds peer AS)")
		}
		if ski := strings.TrimSpace(sec.BGPsec.SKI); ski != "" {
			// 40 hex chars = 20 octets
			if len(ski) != 40 {
				return fmt.Errorf("bgp.security.bgpsec.ski: want 40 hex chars (20 octets), got %d", len(ski))
			}
		}
	}
	if sec.ROV.Enabled && sec.ROV.RequireValid {
		if len(sec.ROV.ROAs) == 0 && strings.TrimSpace(sec.ROV.ROAFile) == "" && !sec.ROV.AllowPrivate {
			return fmt.Errorf("bgp.security.rov: require_valid needs roas, roa_file, or allow_private")
		}
	}
	for i, r := range sec.ROV.ROAs {
		if strings.TrimSpace(r.Prefix) == "" {
			return fmt.Errorf("bgp.security.rov.roas[%d]: prefix required", i)
		}
		if _, _, err := net.ParseCIDR(r.Prefix); err != nil {
			return fmt.Errorf("bgp.security.rov.roas[%d]: %w", i, err)
		}
		if r.ASN == 0 {
			return fmt.Errorf("bgp.security.rov.roas[%d]: asn required", i)
		}
	}
	return nil
}

// FailClosed reports whether security failures must block announce.
func (c *Config) FailClosed() bool {
	if c.BGP.Security.FailClosed == nil {
		return false
	}
	return *c.BGP.Security.FailClosed
}

// PrivateSuffixes returns the split-horizon authoritative suffix list
func (c *Config) PrivateSuffixes() []string {
	return append([]string{}, c.DNS.PrivateSuffixes...)
}

func normalizeTLD(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, ".")
	return s
}

func normalizeSuffix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, ".") {
		s = "." + s
	}
	return s
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
