package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type vpiStubConfig struct {
	Listen          string
	UpstreamNS      string
	FallbackNS      string
	PrivateSuffixes []string
}

func resolveVPIStubConfig() vpiStubConfig {
	if err := loadDNSConfig(); err != nil && !*quiet {
		log.Printf("[dns] %v", err)
	}

	upstream := strings.TrimSpace(*vpiUpstream)
	if upstream == "" {
		upstream = strings.TrimSpace(os.Getenv("TRUST_VPI_UPSTREAM_NS"))
	}
	// Prefer the local mesh authority DNS, then the tunnel server's mesh-dns port.
	if upstream == "" {
		if auth := getGlobalMeshAuthority(); auth != nil && strings.TrimSpace(auth.dnsListen) != "" {
			upstream = auth.dnsListen
		}
	}
	if upstream == "" && meshActive() {
		upstream = meshDNSUpstreamFromServer()
	}
	if upstream == "" {
		upstream = "127.0.0.1:5353"
	}

	file := getDNSFile()
	if file.DefaultUpstream != "" {
		// Explicit YAML default overrides mesh guess when set.
		upstream = file.DefaultUpstream
	}

	tld := strings.ToLower(strings.TrimSpace(*meshTLD))
	suffixes := []string{".mesh", ".social", ".tunnel"}
	if tld != "" && tld != "tunnel" && tld != "mesh" && tld != "social" {
		suffixes = append(suffixes, "."+tld)
	}
	for _, s := range privateSuffixesFromDNSFile(file) {
		if !containsString(suffixes, s) {
			suffixes = append(suffixes, s)
		}
	}

	listen := strings.TrimSpace(*vpiListen)
	if file.Listen != "" {
		listen = file.Listen
	}

	fallback := strings.TrimSpace(*vpiFallback)
	if file.Fallback != "" {
		fallback = file.Fallback
	}

	return vpiStubConfig{
		Listen:          listen,
		UpstreamNS:      upstream,
		FallbackNS:      fallback,
		PrivateSuffixes: suffixes,
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// meshDNSUpstreamFromServer builds host:port for the remote mesh DNS.
// Clients typically reach the server's -mesh-dns (default port 5353).
func meshDNSUpstreamFromServer() string {
	host := strings.TrimSpace(*serverIP)
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	// If mesh-dns is host:port, reuse its port with the tunnel server host.
	listen := strings.TrimSpace(*meshDNS)
	port := "5353"
	if listen != "" {
		if h, p, err := net.SplitHostPort(listen); err == nil {
			port = p
			// When client also runs authority-less, keep configured host if loopback-only not applicable.
			_ = h
		} else if !strings.Contains(listen, ":") {
			port = listen
		}
	}
	return net.JoinHostPort(host, port)
}

func startVPIStub(ctx context.Context) {
	if !vpiActive() {
		return
	}
	cfg := resolveVPIStubConfig()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:5354"
	}
	if cfg.FallbackNS == "" {
		cfg.FallbackNS = "8.8.8.8:53"
	}

	addr, err := net.ResolveUDPAddr("udp", cfg.Listen)
	if err != nil {
		log.Printf("[vpi] stub listen resolve failed: %v", err)
		return
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Printf("[vpi] stub listen failed on %s: %v", cfg.Listen, err)
		return
	}

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	go func() {
		buf := make([]byte, 512)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					select {
					case <-ctx.Done():
						return
					default:
						continue
					}
				}
				return
			}
			query := make([]byte, n)
			copy(query, buf[:n])
			go func(packet []byte, client *net.UDPAddr) {
				resp, via := vpiForward(packet, cfg)
				if len(resp) == 0 {
					return
				}
				_, _ = conn.WriteToUDP(resp, client)
				if via != "" && !*quiet {
					log.Printf("[vpi] resolved via %s", via)
				}
			}(query, remote)
		}
	}()

	zones := getDNSFile().Zones
	zoneNote := ""
	if len(zones) > 0 {
		zoneNote = fmt.Sprintf(", yaml zones: %d", len(zones))
	}
	log.Printf("[vpi] private DNS stub on UDP %s (suffixes: %s, upstream: %s%s)",
		cfg.Listen, strings.Join(cfg.PrivateSuffixes, ", "), cfg.UpstreamNS, zoneNote)
}

func vpiForward(packet []byte, cfg vpiStubConfig) ([]byte, string) {
	domain, err := vpiQueryName(packet)
	if err != nil {
		return nil, ""
	}
	res := resolverForDomain(domain, cfg)
	resp, via, err := vpiExchangeResolver(packet, res)
	if err != nil {
		return nil, ""
	}
	return resp, via
}

func vpiExchangeResolver(packet []byte, res dnsResolver) ([]byte, string, error) {
	// Prefer DoH when configured; fall back to classic UDP on DoH failure if UDP is set.
	if res.DoH != "" {
		resp, err := vpiExchangeDoH(packet, res.DoH, res.DoHMethod)
		if err == nil {
			return resp, res.DoH, nil
		}
		if res.UDP == "" {
			return nil, "", err
		}
		if !*quiet {
			log.Printf("[vpi] doh %s failed: %v — trying udp %s", res.DoH, err, res.UDP)
		}
	}
	if res.UDP == "" {
		return nil, "", fmt.Errorf("no resolver")
	}
	resp, err := vpiExchangeUDP(packet, res.UDP)
	if err != nil {
		return nil, "", err
	}
	return resp, res.UDP, nil
}

func vpiQueryName(packet []byte) (string, error) {
	if len(packet) < 13 {
		return "", fmt.Errorf("short packet")
	}
	idx := 12
	var parts []string
	for {
		if idx >= len(packet) {
			return "", fmt.Errorf("truncated")
		}
		length := int(packet[idx])
		if length == 0 {
			break
		}
		if idx+1+length > len(packet) {
			return "", fmt.Errorf("bad label")
		}
		parts = append(parts, string(packet[idx+1:idx+1+length]))
		idx += 1 + length
	}
	return strings.Join(parts, "."), nil
}

func vpiExchangeUDP(packet []byte, upstream string) ([]byte, error) {
	addr, err := net.ResolveUDPAddr("udp", upstream)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n < 12 {
		return nil, fmt.Errorf("short response")
	}
	if binary.BigEndian.Uint16(buf[2:4])&0x000F != 0 {
		return nil, fmt.Errorf("dns error rcode=%d", binary.BigEndian.Uint16(buf[2:4])&0x000F)
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return out, nil
}

// vpiExchangeDoH performs RFC 8484 DNS-over-HTTPS using application/dns-message.
func vpiExchangeDoH(packet []byte, dohURL, method string) ([]byte, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		method = "post"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	if *insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}

	var req *http.Request
	var err error
	switch method {
	case "get":
		// GET ?dns=base64url(query) without padding
		enc := base64.RawURLEncoding.EncodeToString(packet)
		u, perr := url.Parse(strings.TrimSpace(dohURL))
		if perr != nil {
			return nil, perr
		}
		q := u.Query()
		q.Set("dns", enc)
		u.RawQuery = q.Encode()
		req, err = http.NewRequest(http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
	default:
		req, err = http.NewRequest(http.MethodPost, strings.TrimSpace(dohURL), bytes.NewReader(packet))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/dns-message")
	}
	req.Header.Set("Accept", "application/dns-message")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("doh HTTP %d", resp.StatusCode)
	}
	if len(body) < 12 {
		return nil, fmt.Errorf("short doh response")
	}
	// Soft-check rcode; still return body so clients can see NXDOMAIN etc.
	return body, nil
}
