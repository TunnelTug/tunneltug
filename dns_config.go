package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// DNSZoneConfig routes matching names to a DoH endpoint and/or classic DNS upstream.
// Match order (most specific first): exact domain, domain suffix, then TLD.
type DNSZoneConfig struct {
	// TLD is a single private label (e.g. "tunnel", "corp") — matches name == tld or *.<tld>.
	TLD string `yaml:"tld" json:"tld"`
	// Domains are FQDNs or suffix patterns (e.g. "services.corp", "*.internal.corp", "corp").
	Domains []string `yaml:"domains" json:"domains"`
	// DoH is a DNS-over-HTTPS base URL (RFC 8484), e.g. https://dns.example.com/dns-query.
	DoH string `yaml:"doh" json:"doh"`
	// Upstream is a classic DNS host:port used when DoH is empty (or as DoH fallback on error).
	Upstream string `yaml:"upstream" json:"upstream"`
	// DoHMethod is "post" (default) or "get" for the DoH transport.
	DoHMethod string `yaml:"doh_method" json:"doh_method"`
}

// DNSFile is the on-disk private DNS / DoH stub config (YAML or JSON).
type DNSFile struct {
	// Listen is the local UDP address for the stub (default: -vpi-listen / 127.0.0.1:5354).
	Listen string `yaml:"listen" json:"listen"`
	// Fallback resolves names that match no zone (DoH URL or classic host:port).
	Fallback string `yaml:"fallback" json:"fallback"`
	// DefaultDoH is used when a matched zone has no doh/upstream of its own.
	DefaultDoH string `yaml:"default_doh" json:"default_doh"`
	// DefaultUpstream is classic DNS used when a zone has no resolver and DefaultDoH is empty.
	DefaultUpstream string `yaml:"default_upstream" json:"default_upstream"`
	// Zones maps TLDs/domains to resolvers.
	Zones []DNSZoneConfig `yaml:"zones" json:"zones"`
	// PrivateTLDs are extra private labels treated like built-in .tunnel/.mesh when no zone matches
	// (resolved via default_doh / default_upstream / mesh upstream).
	PrivateTLDs []string `yaml:"private_tlds" json:"private_tlds"`
}

// dnsResolver is the concrete upstream chosen for one query.
type dnsResolver struct {
	Label     string // for logs
	DoH       string
	DoHMethod string
	UDP       string // host:port
}

var (
	dnsMu     sync.RWMutex
	dnsFile   DNSFile
	dnsLoaded bool
)

func dnsConfigPath() string {
	if p := strings.TrimSpace(*dnsFileFlag); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("TUNNELTUG_DNS")); p != "" {
		return p
	}
	return ""
}

func dnsConfigActive() bool {
	return dnsConfigPath() != ""
}

// loadDNSConfig parses -dns / TUNNELTUG_DNS. Empty path clears the runtime table.
func loadDNSConfig() error {
	path := dnsConfigPath()
	if path == "" {
		dnsMu.Lock()
		dnsFile = DNSFile{}
		dnsLoaded = true
		dnsMu.Unlock()
		return nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read dns config %s: %w", path, err)
	}

	var file DNSFile
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("parse dns json %s: %w", path, err)
		}
	default:
		if err := yaml.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("parse dns yaml %s: %w", path, err)
		}
	}

	if err := validateDNSFile(file); err != nil {
		return fmt.Errorf("dns config %s: %w", path, err)
	}
	normalizeDNSFile(&file)

	dnsMu.Lock()
	dnsFile = file
	dnsLoaded = true
	dnsMu.Unlock()

	if !*quiet {
		log.Printf("[dns] loaded %d zone(s) from %s", len(file.Zones), path)
		for _, z := range file.Zones {
			parts := make([]string, 0, 3)
			if z.TLD != "" {
				parts = append(parts, "tld=."+z.TLD)
			}
			if len(z.Domains) > 0 {
				parts = append(parts, "domains="+strings.Join(z.Domains, ","))
			}
			if z.DoH != "" {
				parts = append(parts, "doh="+z.DoH)
			} else if z.Upstream != "" {
				parts = append(parts, "upstream="+z.Upstream)
			}
			log.Printf("[dns]   %s", strings.Join(parts, " "))
		}
	}
	return nil
}

func ensureDNSLoaded() {
	dnsMu.RLock()
	ok := dnsLoaded
	dnsMu.RUnlock()
	if ok {
		return
	}
	if err := loadDNSConfig(); err != nil && !*quiet {
		log.Printf("[dns] load failed: %v", err)
	}
}

func getDNSFile() DNSFile {
	ensureDNSLoaded()
	dnsMu.RLock()
	defer dnsMu.RUnlock()
	return dnsFile
}

func normalizeDNSFile(f *DNSFile) {
	f.Listen = strings.TrimSpace(f.Listen)
	f.Fallback = strings.TrimSpace(f.Fallback)
	f.DefaultDoH = strings.TrimSpace(f.DefaultDoH)
	f.DefaultUpstream = strings.TrimSpace(f.DefaultUpstream)
	for i := range f.PrivateTLDs {
		f.PrivateTLDs[i] = normalizeTLDLabel(f.PrivateTLDs[i])
	}
	for i := range f.Zones {
		z := &f.Zones[i]
		z.TLD = normalizeTLDLabel(z.TLD)
		z.DoH = strings.TrimSpace(z.DoH)
		z.Upstream = strings.TrimSpace(z.Upstream)
		z.DoHMethod = strings.ToLower(strings.TrimSpace(z.DoHMethod))
		if z.DoHMethod == "" {
			z.DoHMethod = "post"
		}
		for j := range z.Domains {
			z.Domains[j] = normalizeDomainPattern(z.Domains[j])
		}
	}
}

func normalizeTLDLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, ".")
	return s
}

func normalizeDomainPattern(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".")
	return s
}

func validateDNSFile(f DNSFile) error {
	if f.DefaultDoH != "" {
		if err := validateDoHURL(f.DefaultDoH); err != nil {
			return fmt.Errorf("default_doh: %w", err)
		}
	}
	if f.Fallback != "" && isDoHURL(f.Fallback) {
		if err := validateDoHURL(f.Fallback); err != nil {
			return fmt.Errorf("fallback: %w", err)
		}
	}
	for i, z := range f.Zones {
		if z.TLD == "" && len(z.Domains) == 0 {
			return fmt.Errorf("zones[%d]: set tld and/or domains", i)
		}
		if z.TLD != "" {
			label := normalizeTLDLabel(z.TLD)
			if label == "" || strings.Contains(label, ".") {
				return fmt.Errorf("zones[%d]: tld %q must be a single label", i, z.TLD)
			}
		}
		if z.DoH != "" {
			if err := validateDoHURL(z.DoH); err != nil {
				return fmt.Errorf("zones[%d].doh: %w", i, err)
			}
		}
		method := strings.ToLower(strings.TrimSpace(z.DoHMethod))
		if method != "" && method != "post" && method != "get" {
			return fmt.Errorf("zones[%d].doh_method %q: use post or get", i, z.DoHMethod)
		}
		if z.DoH == "" && strings.TrimSpace(z.Upstream) == "" && strings.TrimSpace(f.DefaultDoH) == "" && strings.TrimSpace(f.DefaultUpstream) == "" {
			// Allowed: zone can inherit mesh/VPI upstream at runtime.
		}
	}
	for i, tld := range f.PrivateTLDs {
		label := normalizeTLDLabel(tld)
		if label == "" || strings.Contains(label, ".") {
			return fmt.Errorf("private_tlds[%d] %q: use a single label", i, tld)
		}
	}
	return nil
}

func isDoHURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func validateDoHURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("must be http(s) URL, got %q", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("missing host in %q", raw)
	}
	return nil
}

// matchDNSZone picks the most specific zone for domain (lowercase FQDN without trailing dot).
// Specificity: exact domain match > domain suffix > TLD. Longer patterns win ties.
func matchDNSZone(domain string, zones []DNSZoneConfig) (DNSZoneConfig, bool) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" || len(zones) == 0 {
		return DNSZoneConfig{}, false
	}

	type hit struct {
		zone  DNSZoneConfig
		score int
	}
	var best *hit

	consider := func(z DNSZoneConfig, score int) {
		if best == nil || score > best.score {
			best = &hit{zone: z, score: score}
		}
	}

	for _, z := range zones {
		for _, pat := range z.Domains {
			pat = normalizeDomainPattern(pat)
			if pat == "" {
				continue
			}
			if strings.HasPrefix(pat, "*.") {
				suffix := strings.TrimPrefix(pat, "*.")
				if domain == suffix {
					// apex of wildcard zone
					consider(z, 300+len(suffix))
					continue
				}
				if strings.HasSuffix(domain, "."+suffix) {
					consider(z, 200+len(suffix))
				}
				continue
			}
			if domain == pat {
				consider(z, 400+len(pat))
				continue
			}
			if strings.HasSuffix(domain, "."+pat) {
				consider(z, 250+len(pat))
			}
		}
		if tld := z.TLD; tld != "" {
			if domain == tld {
				consider(z, 100+len(tld))
			} else if strings.HasSuffix(domain, "."+tld) {
				// Prefer longer TLD labels slightly; domain matches above still win.
				consider(z, 50+len(tld))
			}
		}
	}
	if best == nil {
		return DNSZoneConfig{}, false
	}
	return best.zone, true
}

// resolverForDomain returns the DoH/UDP target for a query name.
func resolverForDomain(domain string, cfg vpiStubConfig) dnsResolver {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")

	file := getDNSFile()
	if z, ok := matchDNSZone(domain, file.Zones); ok {
		method := z.DoHMethod
		if method == "" {
			method = "post"
		}
		if z.DoH != "" {
			return dnsResolver{Label: "zone-doh", DoH: z.DoH, DoHMethod: method, UDP: z.Upstream}
		}
		if z.Upstream != "" {
			return dnsResolver{Label: "zone-udp", UDP: z.Upstream}
		}
		if file.DefaultDoH != "" {
			return dnsResolver{Label: "default-doh", DoH: file.DefaultDoH, DoHMethod: "post", UDP: file.DefaultUpstream}
		}
		if file.DefaultUpstream != "" {
			return dnsResolver{Label: "default-udp", UDP: file.DefaultUpstream}
		}
		// Zone matched but no resolver: fall through to private mesh upstream.
		if cfg.UpstreamNS != "" {
			return dnsResolver{Label: "mesh-udp", UDP: cfg.UpstreamNS}
		}
	}

	if isPrivateDNSName(domain, cfg) {
		if file.DefaultDoH != "" {
			return dnsResolver{Label: "private-doh", DoH: file.DefaultDoH, DoHMethod: "post", UDP: firstNonEmpty(file.DefaultUpstream, cfg.UpstreamNS)}
		}
		upstream := firstNonEmpty(file.DefaultUpstream, cfg.UpstreamNS)
		if upstream != "" {
			return dnsResolver{Label: "private-udp", UDP: upstream}
		}
	}

	// Public / unmatched names.
	fb := firstNonEmpty(file.Fallback, cfg.FallbackNS)
	if isDoHURL(fb) {
		return dnsResolver{Label: "fallback-doh", DoH: fb, DoHMethod: "post"}
	}
	return dnsResolver{Label: "fallback-udp", UDP: fb}
}

func isPrivateDNSName(domain string, cfg vpiStubConfig) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimSuffix(domain, ".")
	for _, suffix := range cfg.PrivateSuffixes {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix == "" {
			continue
		}
		if !strings.HasPrefix(suffix, ".") {
			suffix = "." + suffix
		}
		if strings.HasSuffix(domain, suffix) || domain == strings.TrimPrefix(suffix, ".") {
			return true
		}
	}
	return false
}

// privateSuffixesFromDNSFile returns .tld entries derived from YAML zones + private_tlds.
func privateSuffixesFromDNSFile(f DNSFile) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(label string) {
		label = normalizeTLDLabel(label)
		if label == "" {
			return
		}
		suf := "." + label
		if _, ok := seen[suf]; ok {
			return
		}
		seen[suf] = struct{}{}
		out = append(out, suf)
	}
	for _, tld := range f.PrivateTLDs {
		add(tld)
	}
	for _, z := range f.Zones {
		if z.TLD != "" {
			add(z.TLD)
		}
		// Also treat multi-label domain roots as private when listed without wildcard.
		for _, d := range z.Domains {
			d = normalizeDomainPattern(d)
			d = strings.TrimPrefix(d, "*.")
			if d == "" {
				continue
			}
			// Use last label as TLD-style suffix for private classification.
			if i := strings.LastIndex(d, "."); i >= 0 {
				add(d[i+1:])
			} else {
				add(d)
			}
		}
	}
	return out
}
