package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const snapshotVersion = 1

// SnapshotTunnel is durable tunnel inventory (live sessions cannot be restored).
type SnapshotTunnel struct {
	Namespace   string    `json:"namespace"`
	Subdomain   string    `json:"subdomain"`
	TunnelKey   string    `json:"tunnel_key"`
	Remote      string    `json:"remote,omitempty"`
	ConnectedAt time.Time `json:"connected_at,omitempty"`
}

// SnapshotIdentity identifies which barge/server replica owns the snapshot.
type SnapshotIdentity struct {
	ControlPort string `json:"control_port"`
	PublicPort  string `json:"public_port"`
	RegisterHost string `json:"register_host,omitempty"`
	FleetID     string `json:"fleet_id,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
}

// BargeSnapshot is written before updates/shutdown and loaded on restore.
type BargeSnapshot struct {
	Version   int              `json:"version"`
	TakenAt   time.Time        `json:"taken_at"`
	Mode      string           `json:"mode"`
	Runtime   string           `json:"runtime,omitempty"`
	Identity  SnapshotIdentity `json:"identity"`
	Tunnels   []SnapshotTunnel `json:"tunnels"`
	Pending   []SnapshotTunnel `json:"pending,omitempty"`
	LB        string           `json:"lb,omitempty"`
	Mesh      bool             `json:"mesh"`
	Domain    string           `json:"domain,omitempty"`
	Routing   string           `json:"routing,omitempty"`
	VersionTT string           `json:"tunneltug_version,omitempty"`
}

func snapshotActive() bool {
	return strings.TrimSpace(*snapshotDir) != ""
}

func resolveSnapshotDir() string {
	dir := strings.TrimSpace(*snapshotDir)
	if dir == "" {
		return ""
	}
	if dir == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return dir
	}
	if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, dir[2:])
		}
	}
	return dir
}

func snapshotIdentity() SnapshotIdentity {
	host, _ := os.Hostname()
	regHost := strings.TrimSpace(*registerHost)
	if regHost == "" {
		regHost = strings.TrimSpace(*bargeHost)
	}
	fleet := strings.TrimSpace(*registerFleetID)
	if fleet == "" {
		fleet = strings.TrimSpace(*bargeFleetID)
	}
	if fleet == "" {
		fleet = strings.TrimSpace(host)
	}
	return SnapshotIdentity{
		ControlPort:  strings.TrimSpace(*controlPort),
		PublicPort:   strings.TrimSpace(*publicPort),
		RegisterHost: regHost,
		FleetID:      fleet,
		Namespace:    normalizeNamespace(*namespace),
		Hostname:     strings.TrimSpace(host),
	}
}

func snapshotIDKey(id SnapshotIdentity) string {
	parts := []string{"ctrl" + id.ControlPort}
	if id.FleetID != "" {
		parts = append(parts, sanitizeSnapshotName(id.FleetID))
	}
	if id.Hostname != "" {
		parts = append(parts, sanitizeSnapshotName(id.Hostname))
	}
	return strings.Join(parts, "_")
}

func sanitizeSnapshotName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "default"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func (m *ServerManager) buildSnapshot() BargeSnapshot {
	id := snapshotIdentity()
	m.mu.RLock()
	defer m.mu.RUnlock()

	tunnels := make([]SnapshotTunnel, 0, len(m.tunnels))
	for key, t := range m.tunnels {
		tunnels = append(tunnels, SnapshotTunnel{
			Namespace:   t.Namespace,
			Subdomain:   t.Subdomain,
			TunnelKey:   key,
			Remote:      t.Remote,
			ConnectedAt: t.ConnectedAt,
		})
	}
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].TunnelKey < tunnels[j].TunnelKey })

	pending := make([]SnapshotTunnel, 0, len(m.pending))
	for _, t := range m.pending {
		pending = append(pending, t)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].TunnelKey < pending[j].TunnelKey })

	lb := strings.TrimSpace(*registerLB)
	if lb == "" {
		lb = strings.TrimSpace(*bargeLB)
	}

	return BargeSnapshot{
		Version:   snapshotVersion,
		TakenAt:   time.Now().UTC(),
		Mode:      "server",
		Runtime:   bargeRuntimeMode(),
		Identity:  id,
		Tunnels:   tunnels,
		Pending:   pending,
		LB:        lb,
		Mesh:      meshAuthorityActive(),
		Domain:    strings.TrimSpace(*domain),
		Routing:   strings.TrimSpace(*routing),
		VersionTT: versionString(),
	}
}

func writeBargeSnapshot(snap BargeSnapshot) (string, error) {
	dir := resolveSnapshotDir()
	if dir == "" {
		return "", fmt.Errorf("snapshot-dir not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	key := snapshotIDKey(snap.Identity)
	name := fmt.Sprintf("barge-%s-%s.json", key, snap.TakenAt.Format("20060102T150405Z"))
	path := filepath.Join(dir, name)

	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	// Also write a stable "latest" pointer for this identity.
	latest := filepath.Join(dir, "barge-"+key+"-latest.json")
	_ = os.WriteFile(latest, raw, 0o600)

	pruneSnapshots(dir, key, *snapshotKeep)
	return path, nil
}

func pruneSnapshots(dir, key string, keep int) {
	if keep < 1 {
		keep = 1
	}
	prefix := "barge-" + key + "-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.HasSuffix(name, "-latest.json") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	if len(files) <= keep {
		return
	}
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(f)
	}
}

func loadLatestSnapshot(id SnapshotIdentity) (*BargeSnapshot, string, error) {
	dir := resolveSnapshotDir()
	if dir == "" {
		return nil, "", fmt.Errorf("snapshot-dir not set")
	}
	key := snapshotIDKey(id)
	latest := filepath.Join(dir, "barge-"+key+"-latest.json")
	if raw, err := os.ReadFile(latest); err == nil {
		var snap BargeSnapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return nil, latest, err
		}
		return &snap, latest, nil
	}

	// Fallback: newest timestamped file for this identity.
	prefix := "barge-" + key + "-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.HasSuffix(name, "-latest.json") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	if len(files) == 0 {
		return nil, "", nil
	}
	sort.Strings(files)
	path := files[len(files)-1]
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, path, err
	}
	var snap BargeSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, path, err
	}
	return &snap, path, nil
}

// restoreSnapshot re-publishes mesh records and marks tunnels as pending reconnect.
// Live QUIC/yamux sessions cannot be restored; clients reconnect to the control port.
func (m *ServerManager) restoreSnapshot(snap *BargeSnapshot, path string) {
	if snap == nil {
		return
	}
	m.mu.Lock()
	if m.pending == nil {
		m.pending = make(map[string]SnapshotTunnel)
	}
	for _, t := range snap.Tunnels {
		key := t.TunnelKey
		if key == "" {
			key = composeTunnelKey(t.Namespace, t.Subdomain)
		}
		t.TunnelKey = key
		m.pending[key] = t
	}
	// Pending from previous cycle still expected.
	for _, t := range snap.Pending {
		key := t.TunnelKey
		if key == "" {
			key = composeTunnelKey(t.Namespace, t.Subdomain)
		}
		t.TunnelKey = key
		if _, live := m.tunnels[key]; !live {
			m.pending[key] = t
		}
	}
	m.restoredFrom = path
	pendingCount := len(m.pending)
	m.mu.Unlock()

	for _, t := range snap.Tunnels {
		publishTunnelToMesh(t.Namespace, t.Subdomain)
	}
	for _, t := range snap.Pending {
		publishTunnelToMesh(t.Namespace, t.Subdomain)
	}

	if !*quiet {
		log.Printf("Restored snapshot %s (%d tunnel(s) pending client reconnect)", path, pendingCount)
	}
}

func (m *ServerManager) clearPendingTunnel(key string) {
	m.mu.Lock()
	delete(m.pending, key)
	m.mu.Unlock()
}

func (m *ServerManager) takeAndWriteSnapshot() (string, error) {
	if !snapshotActive() {
		return "", fmt.Errorf("snapshots disabled (set -snapshot-dir)")
	}
	snap := m.buildSnapshot()
	path, err := writeBargeSnapshot(snap)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.lastSnapshot = path
	m.mu.Unlock()
	return path, nil
}

func (m *ServerManager) maybeRestoreOnStart() {
	if !snapshotActive() || !*snapshotRestore {
		return
	}
	snap, path, err := loadLatestSnapshot(snapshotIdentity())
	if err != nil {
		log.Printf("Snapshot restore: %v", err)
		return
	}
	if snap == nil {
		return
	}
	m.restoreSnapshot(snap, path)
}

func (m *ServerManager) maybeSnapshotOnShutdown() {
	if !snapshotActive() || !*snapshotOnShutdown {
		return
	}
	path, err := m.takeAndWriteSnapshot()
	if err != nil {
		log.Printf("Snapshot on shutdown failed: %v", err)
		return
	}
	if !*quiet {
		log.Printf("Wrote barge snapshot before shutdown: %s", path)
	}
}

func (m *ServerManager) runPeriodicSnapshots(ctx context.Context) {
	if !snapshotActive() || *snapshotInterval < 1 {
		return
	}
	ticker := time.NewTicker(time.Duration(*snapshotInterval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			path, err := m.takeAndWriteSnapshot()
			if err != nil {
				if !*quiet {
					log.Printf("Periodic snapshot failed: %v", err)
				}
				continue
			}
			if !*quiet {
				log.Printf("Periodic barge snapshot: %s", path)
			}
		}
	}
}

func snapshotStatus(m *ServerManager) map[string]any {
	if m == nil {
		return map[string]any{"enabled": snapshotActive()}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	pending := make([]string, 0, len(m.pending))
	for k := range m.pending {
		pending = append(pending, k)
	}
	sort.Strings(pending)
	return map[string]any{
		"enabled":       snapshotActive(),
		"dir":           resolveSnapshotDir(),
		"last_snapshot": m.lastSnapshot,
		"restored_from": m.restoredFrom,
		"pending":       pending,
		"live_tunnels":  len(m.tunnels),
	}
}

func snapshotRequestAuthorized(r *http.Request) bool {
	if tokensEqual(r.Header.Get("X-TunnelTug-Token"), *authToken) {
		return true
	}
	if tokensEqual(r.URL.Query().Get("token"), *authToken) {
		return true
	}
	return false
}

func (m *ServerManager) mountSnapshotHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/_tunneltug/snapshot", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			payload := map[string]any{
				"status":   "ok",
				"snapshot": snapshotStatus(m),
			}
			if snapshotRequestAuthorized(r) {
				payload["current"] = m.buildSnapshot()
			}
			_ = json.NewEncoder(w).Encode(payload)
		case http.MethodPost:
			if !snapshotRequestAuthorized(r) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			path, err := m.takeAndWriteSnapshot()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"path":   path,
				"snap":   m.buildSnapshot(),
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
