package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

func tlsHosts() []string {
	seen := make(map[string]struct{})
	hosts := make([]string, 0, 4)

	add := func(host string) {
		host = strings.TrimSpace(host)
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}

	add(*domain)
	// Direct prod/dev: apex (and optional www) is enough for a single shared tunnel.
	// Subdomain routing still needs -subalt '*.example.com' (or per-host names) for ACME.
	if isDirectRouting() {
		if d := strings.TrimSpace(*domain); d != "" {
			add("www." + d)
		}
	}
	for _, host := range strings.Split(*subalt, ",") {
		add(host)
	}
	// Product vhosts (apex + optional *.domain for wildcard CMS products).
	for _, host := range vhostACMEHosts() {
		add(host)
	}

	return hosts
}

func buildCertProvider() *certProvider {
	switch {
	case *prod:
		hosts := tlsHosts()
		log.Printf("Production TLS: requesting ACME certificates for %s", strings.Join(hosts, ", "))

		cacheDir := strings.TrimSpace(*acmeCache)
		if cacheDir == "" {
			cacheDir = "certs-cache"
		}
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(hosts...),
			Cache:      autocert.DirCache(cacheDir),
		}
		if strings.TrimSpace(*email) != "" {
			manager.Email = strings.TrimSpace(*email)
		}

		tlsConfig := manager.TLSConfig()
		tlsConfig.MinVersion = tls.VersionTLS12
		return &certProvider{
			controlTLS: tlsConfig,
			publicTLS:  tlsConfig,
			acmeMgr:    manager,
		}

	case *dev:
		hosts := tlsHosts()
		hosts = append(hosts, "localhost", "127.0.0.1")
		log.Printf("Development TLS: generating self-signed certificate for %s", strings.Join(hosts, ", "))

		tlsConfig, err := generateSelfSignedCert(hosts)
		if err != nil {
			log.Fatalf("Failed to generate development certificate: %v", err)
		}
		return &certProvider{
			controlTLS: tlsConfig,
			publicTLS:  tlsConfig,
		}

	default:
		tlsConfig := legacyTLSConfig()
		return &certProvider{
			controlTLS: tlsConfig,
		}
	}
}

func clientTLSConfig() *tls.Config {
	cfg := &tls.Config{
		InsecureSkipVerify: *insecure,
		MinVersion:         tls.VersionTLS12,
	}
	if strings.TrimSpace(*domain) != "" {
		cfg.ServerName = strings.TrimSpace(*domain)
	}
	return cfg
}

func backendTLSConfig() *tls.Config {
	cfg := &tls.Config{
		InsecureSkipVerify: *backendInsecure,
		MinVersion:         tls.VersionTLS12,
	}
	if strings.TrimSpace(*domain) != "" {
		cfg.ServerName = strings.TrimSpace(*domain)
	}
	return cfg
}

func legacyTLSConfig() *tls.Config {
	if *certFile != "" && *keyFile != "" {
		cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			log.Fatalf("Failed to load TLS certs: %v", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	log.Println("No TLS certificates provided. Auto-generating temporary self-signed certs...")
	tlsConfig, err := generateSelfSignedCert([]string{"localhost", "127.0.0.1"})
	if err != nil {
		log.Fatalf("Failed to generate TLS certificate: %v", err)
	}
	return tlsConfig
}

func generateSelfSignedCert(hosts []string) (*tls.Config, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"TunnelTug"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
			continue
		}
		template.DNSNames = append(template.DNSNames, host)
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}

	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}),
	)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
