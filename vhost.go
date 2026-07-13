package main

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// VHostConfig maps a public product domain to a local (or cloud://) upstream.
// Same shape as 0trust-services product vhosts so configs can be shared.
type VHostConfig struct {
	Domain             string `yaml:"domain" json:"domain"`
	Upstream           string `yaml:"upstream" json:"upstream"`
	AuthProxy          bool   `yaml:"auth_proxy" json:"auth_proxy"`
	WildcardSubdomains bool   `yaml:"wildcard_subdomains" json:"wildcard_subdomains"`
}

// VHostIdentity is the optional identity plane used when auth_proxy is true.
type VHostIdentity struct {
	PlatformURL      string `yaml:"platform_url" json:"platform_url"`
	PlatformUpstream string `yaml:"platform_upstream" json:"platform_upstream"`
	CloudDomain      string `yaml:"cloud_domain" json:"cloud_domain"`
	CloudBackhaul    string `yaml:"cloud_backhaul" json:"cloud_backhaul"`
}

// VHostFile is the on-disk vhost edge config (YAML or JSON).
type VHostFile struct {
	PlatformURL      string        `yaml:"platform_url" json:"platform_url"`
	PlatformUpstream string        `yaml:"platform_upstream" json:"platform_upstream"`
	CloudDomain      string        `yaml:"cloud_domain" json:"cloud_domain"`
	CloudBackhaul    string        `yaml:"cloud_backhaul" json:"cloud_backhaul"`
	ACMEDomains      []string      `yaml:"acme_domains" json:"acme_domains"`
	VHosts           []VHostConfig `yaml:"vhosts" json:"vhosts"`
}

func (f VHostFile) identity() VHostIdentity {
	return VHostIdentity{
		PlatformURL:      f.PlatformURL,
		PlatformUpstream: f.PlatformUpstream,
		CloudDomain:      f.CloudDomain,
		CloudBackhaul:    f.CloudBackhaul,
	}
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func hostMatchesDomain(host, domain string) bool {
	host = normalizeHost(host)
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// hostMatchesVHost matches only apex (and www) product hosts — not user tunnel subdomains.
func hostMatchesVHost(host, domain string) bool {
	return hostMatchesVHostEntry(host, VHostConfig{Domain: domain})
}

func hostMatchesVHostEntry(host string, vh VHostConfig) bool {
	domain := strings.ToLower(strings.TrimSpace(vh.Domain))
	host = normalizeHost(host)
	if domain == "" {
		return false
	}
	if host == domain || host == "www."+domain {
		return true
	}
	if !vh.WildcardSubdomains {
		return false
	}
	suffix := "." + domain
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	prefix := strings.TrimSuffix(host, suffix)
	// Single label only: app.motionkb.com yes; deep.sub.motionkb.com no.
	if prefix == "" || strings.Contains(prefix, ".") {
		return false
	}
	return true
}

func expandCloudUpstream(id VHostIdentity, upstream string) string {
	if !strings.HasPrefix(upstream, "cloud://") {
		return upstream
	}
	base := strings.TrimRight(strings.TrimSpace(id.CloudBackhaul), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(id.PlatformUpstream), "/")
	}
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(id.PlatformURL), "/")
	}
	port := strings.TrimPrefix(upstream, "cloud://")
	if base == "" {
		return ""
	}
	return base + ":" + port
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Caller should skip invalid upstreams; return a dummy that fails later.
		return &url.URL{Scheme: "http", Host: "127.0.0.1:9"}
	}
	return u
}

func buildVHostHandlers(id VHostIdentity, vhosts []VHostConfig) map[string]http.Handler {
	handlers := make(map[string]http.Handler)
	for _, vh := range vhosts {
		domain := strings.ToLower(strings.TrimSpace(vh.Domain))
		upstream := expandCloudUpstream(id, strings.TrimSpace(vh.Upstream))
		if domain == "" || upstream == "" {
			continue
		}
		target, err := url.Parse(upstream)
		if err != nil || target.Scheme == "" || target.Host == "" {
			continue
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.FlushInterval = -1
		origDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			origDirector(req)
			req.Header.Set("X-Forwarded-Proto", "https")
			if req.Header.Get("X-Forwarded-Host") == "" {
				req.Header.Set("X-Forwarded-Host", req.Host)
			}
			req.Header.Set("X-0Trust-VPI", "1")
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "vhost upstream unavailable", http.StatusBadGateway)
		}
		handlers[domain] = wrapVHostWithCloudAuth(id, vh, proxy)
	}
	return handlers
}

func matchVHostHandler(host string, vhosts []VHostConfig, handlers map[string]http.Handler) http.Handler {
	for _, vh := range vhosts {
		domain := strings.ToLower(strings.TrimSpace(vh.Domain))
		if h, ok := handlers[domain]; ok && hostMatchesVHostEntry(host, vh) {
			return h
		}
	}
	return nil
}

// collectVHostACMEDomains returns product apex domains that need certificates.
func collectVHostACMEDomains(file VHostFile) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(d string) {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, d := range file.ACMEDomains {
		add(d)
	}
	for _, vh := range file.VHosts {
		add(vh.Domain)
		// Note: single-label wildcards (wildcard_subdomains) need extra hosts in
		// acme_domains or -subalt; HTTP-01 cannot issue *.domain automatically.
	}
	return out
}
