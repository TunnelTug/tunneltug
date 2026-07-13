package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// VHostUpstreamRequest sets a product domain's upstream without changing the public hostname.
// Internet still sees williwaw.app / tunneltug.com — only the reverse-proxy target moves
// (e.g. primary app vs standby k3s barge on 0trust.services).
type VHostUpstreamRequest struct {
	Token    string `json:"token"`
	Domain   string `json:"domain"`
	Upstream string `json:"upstream"`
	// Mode is audit-only: primary | standby
	Mode string `json:"mode,omitempty"`
}

type VHostUpstreamResponse struct {
	Status   string       `json:"status"`
	Domain   string       `json:"domain,omitempty"`
	Upstream string       `json:"upstream,omitempty"`
	Mode     string       `json:"mode,omitempty"`
	VHosts   []VHostConfig `json:"vhosts,omitempty"`
	Error    string       `json:"error,omitempty"`
}

func mountVHostAdminHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/_tunneltug/vhosts", handleVHostsList)
	mux.HandleFunc("/_tunneltug/vhosts/upstream", handleVHostUpstream)
	mux.HandleFunc("/_tunneltug/vhosts/reload", handleVHostReload)
}

func vhostAdminAuthorized(r *http.Request, bodyToken string) bool {
	if tokensEqual(bodyToken, *authToken) {
		return true
	}
	if tokensEqual(r.Header.Get("X-TunnelTug-Token"), *authToken) {
		return true
	}
	if tokensEqual(r.URL.Query().Get("token"), *authToken) {
		return true
	}
	return false
}

func handleVHostsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !vhostAdminAuthorized(r, "") {
		writeVHostJSON(w, http.StatusUnauthorized, VHostUpstreamResponse{Error: "unauthorized"})
		return
	}
	writeVHostJSON(w, http.StatusOK, VHostUpstreamResponse{
		Status: "ok",
		VHosts: snapshotVHosts(),
	})
}

func handleVHostReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !vhostAdminAuthorized(r, "") {
		writeVHostJSON(w, http.StatusUnauthorized, VHostUpstreamResponse{Error: "unauthorized"})
		return
	}
	if err := loadVHosts(); err != nil {
		writeVHostJSON(w, http.StatusBadRequest, VHostUpstreamResponse{Error: err.Error()})
		return
	}
	writeVHostJSON(w, http.StatusOK, VHostUpstreamResponse{Status: "ok", VHosts: snapshotVHosts()})
}

func handleVHostUpstream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req VHostUpstreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeVHostJSON(w, http.StatusBadRequest, VHostUpstreamResponse{Error: "invalid json"})
		return
	}
	if !vhostAdminAuthorized(r, req.Token) {
		writeVHostJSON(w, http.StatusUnauthorized, VHostUpstreamResponse{Error: "unauthorized"})
		return
	}
	domain := normalizeHost(req.Domain)
	upstream := strings.TrimSpace(req.Upstream)
	if domain == "" || upstream == "" {
		writeVHostJSON(w, http.StatusBadRequest, VHostUpstreamResponse{Error: "domain and upstream required"})
		return
	}

	if err := setVHostUpstream(domain, upstream); err != nil {
		writeVHostJSON(w, http.StatusBadRequest, VHostUpstreamResponse{Error: err.Error()})
		return
	}
	if !*quiet {
		log.Printf("[vhost] failover cutover domain=%s upstream=%s mode=%s (public hostname unchanged)",
			domain, upstream, strings.TrimSpace(req.Mode))
	}
	writeVHostJSON(w, http.StatusOK, VHostUpstreamResponse{
		Status:   "ok",
		Domain:   domain,
		Upstream: upstream,
		Mode:     req.Mode,
		VHosts:   snapshotVHosts(),
	})
}

func snapshotVHosts() []VHostConfig {
	ensureVHostsLoaded()
	vhostMu.RLock()
	defer vhostMu.RUnlock()
	return append([]VHostConfig(nil), vhostFile.VHosts...)
}

// setVHostUpstream updates memory + on-disk vhosts file, then rebuilds handlers.
// Public Host stays the same; only reverse-proxy target changes.
func setVHostUpstream(domain, upstream string) error {
	path := vhostConfigPath()
	if path == "" {
		return fmt.Errorf("no -vhosts / TUNNELTUG_VHOSTS file configured")
	}
	ensureVHostsLoaded()

	vhostMu.Lock()
	found := false
	for i := range vhostFile.VHosts {
		d := strings.ToLower(strings.TrimSpace(vhostFile.VHosts[i].Domain))
		if d == domain || d == "www."+domain {
			vhostFile.VHosts[i].Upstream = upstream
			found = true
		}
	}
	if !found {
		vhostFile.VHosts = append(vhostFile.VHosts, VHostConfig{
			Domain:   domain,
			Upstream: upstream,
		})
	}
	fileCopy := vhostFile
	vhostMu.Unlock()

	if err := persistVHostFile(path, fileCopy); err != nil {
		return err
	}
	// Rebuild proxies from updated file
	return loadVHosts()
}

func persistVHostFile(path string, file VHostFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Keep a timestamped backup of previous config (never lose primary routes)
	if prev, err := os.ReadFile(path); err == nil && len(prev) > 0 {
		bak := path + ".bak." + time.Now().UTC().Format("20060102T150405Z")
		_ = os.WriteFile(bak, prev, 0o600)
	}

	ext := strings.ToLower(filepath.Ext(path))
	var raw []byte
	var err error
	if ext == ".json" {
		raw, err = json.MarshalIndent(file, "", "  ")
	} else {
		raw, err = yaml.Marshal(file)
	}
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeVHostJSON(w http.ResponseWriter, code int, resp VHostUpstreamResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}
