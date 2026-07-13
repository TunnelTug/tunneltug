package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWriteAndLoadBargeSnapshot(t *testing.T) {
	resetFlags(t)
	dir := t.TempDir()
	*snapshotDir = dir
	*controlPort = "9001"
	*publicPort = "8445"
	*registerHost = "10.0.0.5"
	*registerFleetID = "edge-0"
	*authToken = "tokentokentoken"
	*snapshotKeep = 3

	m := &ServerManager{
		tunnels: map[string]*liveTunnel{
			"myapp": {
				Namespace:   "default",
				Subdomain:   "myapp",
				Remote:      "1.2.3.4:1234",
				ConnectedAt: time.Now().UTC(),
			},
		},
		pending: make(map[string]SnapshotTunnel),
	}

	path, err := m.takeAndWriteSnapshot()
	if err != nil {
		t.Fatalf("takeAndWriteSnapshot: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}

	snap, loaded, err := loadLatestSnapshot(snapshotIdentity())
	if err != nil {
		t.Fatalf("loadLatestSnapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if loaded == "" {
		t.Fatal("expected path")
	}
	if len(snap.Tunnels) != 1 || snap.Tunnels[0].TunnelKey != "myapp" {
		t.Fatalf("tunnels: %+v", snap.Tunnels)
	}
	if snap.Identity.ControlPort != "9001" {
		t.Fatalf("identity: %+v", snap.Identity)
	}

	// latest pointer
	latest := filepath.Join(dir, "barge-"+snapshotIDKey(snapshotIdentity())+"-latest.json")
	if _, err := os.Stat(latest); err != nil {
		t.Fatalf("latest missing: %v", err)
	}
}

func TestRestoreSnapshotMarksPending(t *testing.T) {
	resetFlags(t)
	m := &ServerManager{
		tunnels: make(map[string]*liveTunnel),
		pending: make(map[string]SnapshotTunnel),
	}
	snap := &BargeSnapshot{
		Version: snapshotVersion,
		TakenAt: time.Now().UTC(),
		Tunnels: []SnapshotTunnel{
			{Namespace: "default", Subdomain: "app1", TunnelKey: "app1"},
			{Namespace: "prod", Subdomain: "api", TunnelKey: "prod/api"},
		},
	}
	m.restoreSnapshot(snap, "/tmp/test.json")
	if len(m.pending) != 2 {
		t.Fatalf("pending=%d want 2", len(m.pending))
	}
	if m.restoredFrom != "/tmp/test.json" {
		t.Fatalf("restoredFrom=%q", m.restoredFrom)
	}

	// Live connect clears pending.
	m.mu.Lock()
	m.tunnels["app1"] = &liveTunnel{Subdomain: "app1", Namespace: "default"}
	delete(m.pending, "app1")
	m.mu.Unlock()
	if _, ok := m.pending["app1"]; ok {
		t.Fatal("app1 should no longer be pending")
	}
	if _, ok := m.pending["prod/api"]; !ok {
		t.Fatal("prod/api should still be pending")
	}
}

func TestPruneSnapshots(t *testing.T) {
	dir := t.TempDir()
	key := "ctrl9001_edge"
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "barge-"+key+"-2026010"+strconv.Itoa(i+1)+".json")
		if err := os.WriteFile(name, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneSnapshots(dir, key, 2)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("kept %d files, want 2", len(entries))
	}
}
