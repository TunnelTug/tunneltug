package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Global product-vhost edge (loaded once at process start for server/lb modes).
var (
	vhostMu       sync.RWMutex
	vhostFile     VHostFile
	vhostHandlers map[string]http.Handler
	vhostLoaded   bool
)

func vhostConfigPath() string {
	if p := strings.TrimSpace(*vhostsFile); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("TUNNELTUG_VHOSTS")); p != "" {
		return p
	}
	return ""
}

// loadVHosts parses -vhosts / TUNNELTUG_VHOSTS and builds reverse-proxy handlers.
// Safe to call multiple times; reloads replace the runtime table.
func loadVHosts() error {
	path := vhostConfigPath()
	if path == "" {
		vhostMu.Lock()
		vhostFile = VHostFile{}
		vhostHandlers = nil
		vhostLoaded = true
		vhostMu.Unlock()
		return nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read vhosts config %s: %w", path, err)
	}

	var file VHostFile
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("parse vhosts json %s: %w", path, err)
		}
	default:
		// .yaml / .yml / no extension
		if err := yaml.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("parse vhosts yaml %s: %w", path, err)
		}
	}

	handlers := buildVHostHandlers(file.identity(), file.VHosts)

	vhostMu.Lock()
	vhostFile = file
	vhostHandlers = handlers
	vhostLoaded = true
	vhostMu.Unlock()

	if len(file.VHosts) > 0 && !*quiet {
		log.Printf("[vhost] loaded %d product vhost(s) from %s", len(handlers), path)
		for domain := range handlers {
			log.Printf("[vhost]   %s", domain)
		}
	}
	return nil
}

func ensureVHostsLoaded() {
	vhostMu.RLock()
	ok := vhostLoaded
	vhostMu.RUnlock()
	if ok {
		return
	}
	if err := loadVHosts(); err != nil && !*quiet {
		log.Printf("[vhost] load failed: %v", err)
	}
}

func matchProductVHost(host string) http.Handler {
	ensureVHostsLoaded()
	vhostMu.RLock()
	defer vhostMu.RUnlock()
	if len(vhostHandlers) == 0 {
		return nil
	}
	return matchVHostHandler(host, vhostFile.VHosts, vhostHandlers)
}

func vhostCount() int {
	ensureVHostsLoaded()
	vhostMu.RLock()
	defer vhostMu.RUnlock()
	return len(vhostHandlers)
}

// vhostACMEHosts returns extra cert names from the vhost config for -prod TLS.
func vhostACMEHosts() []string {
	ensureVHostsLoaded()
	vhostMu.RLock()
	defer vhostMu.RUnlock()
	return collectVHostACMEDomains(vhostFile)
}
