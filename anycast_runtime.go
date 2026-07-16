package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"tunneltug/anycast/bgpsec"
	anycastconfig "tunneltug/anycast/config"
	"tunneltug/anycast/updater"
)

func anycastConfigPath() string {
	if p := strings.TrimSpace(*anycastConfig); p != "" {
		return p
	}
	return strings.TrimSpace(os.Getenv("TUNNELTUG_ANYCAST_CONFIG"))
}

func anycastStandalone() bool {
	return strings.ToLower(strings.TrimSpace(*mode)) == "anycast"
}

// maybeGenBGPsecKey handles -anycast-gen-bgpsec-key (writes PEM, prints SKI).
func maybeGenBGPsecKey() bool {
	path := strings.TrimSpace(*anycastGenBGPsecKey)
	if path == "" {
		return false
	}
	pem, ski, err := bgpsec.GenerateKeyPEM()
	if err != nil {
		log.Fatalf("bgpsec keygen: %v", err)
	}
	if d := filepath.Dir(path); d != "" && d != "." {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatalf("bgpsec keygen mkdir: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte(pem), 0o600); err != nil {
		log.Fatalf("bgpsec keygen write: %v", err)
	}
	fmt.Printf("wrote BGPsec router private key (ECDSA P-256) to %s\n", path)
	fmt.Printf("ski=%s\n", ski)
	fmt.Printf("note: enroll this public key under RPKI as a BGPsec router certificate for your ASN.\n")
	fmt.Printf("      Do not reuse ACME/TLS private keys for BGPsec.\n")
	return true
}

// loadAnycastConfig loads and validates the anycast YAML.
func loadAnycastConfig() (*anycastconfig.Config, error) {
	path := anycastConfigPath()
	if path == "" {
		return nil, fmt.Errorf("-anycast-config (or TUNNELTUG_ANYCAST_CONFIG) is required")
	}
	return anycastconfig.Load(path)
}

// runAnycast is -mode anycast: BGP health-gated split-horizon edge (former standalone anycast binary).
func runAnycast() {
	cfg, err := loadAnycastConfig()
	if err != nil {
		log.Fatalf("anycast config: %v", err)
	}

	ctx, stop := notifyShutdownContext()
	defer stop()

	u, err := updater.New(cfg)
	if err != nil {
		log.Fatalf("anycast: %v", err)
	}

	log.Printf("Starting anycast edge %s [node=%s backend=%s tlds=%v bgpsec=%v rov=%v]",
		Version, cfg.NodeID, cfg.BGP.Backend, cfg.TLDs,
		cfg.BGP.Security.BGPsec.Enabled, cfg.BGP.Security.ROV.Enabled)
	if err := u.Run(ctx); err != nil {
		log.Fatalf("anycast: %v", err)
	}
}

// startAnycastSideCar runs the anycast updater in the background for server/lb modes.
func startAnycastSideCar(parent context.Context) (stop func()) {
	if !*anycastEnable {
		return func() {}
	}
	cfg, err := loadAnycastConfig()
	if err != nil {
		log.Fatalf("anycast config: %v", err)
	}
	ctx, cancel := context.WithCancel(parent)
	u, err := updater.New(cfg)
	if err != nil {
		cancel()
		log.Fatalf("anycast: %v", err)
	}
	go func() {
		log.Printf("Starting anycast sidecar [node=%s backend=%s bgpsec=%v rov=%v]",
			cfg.NodeID, cfg.BGP.Backend, cfg.BGP.Security.BGPsec.Enabled, cfg.BGP.Security.ROV.Enabled)
		if err := u.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("anycast sidecar stopped: %v", err)
		}
	}()
	return cancel
}
