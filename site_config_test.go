package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandSite_FullMeshKernelPeers(t *testing.T) {
	cfg := &SiteConfig{
		Site: SiteMeta{Name: "global", Domain: "example.com", PublicScheme: "https"},
		KernelMesh: &KernelMeshPolicy{Mode: "full-mesh"},
		Stack: &StackConfigFile{
			Namespace: "0trust-stack",
			Barges: []BargeProductConfig{
				{Name: "ultimate_db"},
				{Name: "williwaw"},
			},
		},
		Pops: []PopConfig{
			{
				ID: "sfo",
				Roles: []string{"stack", "kernel", "anycast"},
				Domain: "sfo.example.com",
				Kernel: &PopKernel{
					UltimateDB: &KernelNode{NodeID: "udb-sfo", URL: "https://kernel-db.sfo.example.com:8480"},
					UltimateKeystore: &KernelNode{NodeID: "uks-sfo", URL: "https://kernel-ks.sfo.example.com:8481"},
				},
			},
			{
				ID: "ams",
				Roles: []string{"stack", "kernel"},
				Domain: "ams.example.com",
				Kernel: &PopKernel{
					UltimateDB: &KernelNode{NodeID: "udb-ams", URL: "https://kernel-db.ams.example.com:8480"},
					UltimateKeystore: &KernelNode{NodeID: "uks-ams", URL: "https://kernel-ks.ams.example.com:8481"},
				},
			},
		},
	}

	plan, err := ExpandSite(cfg, "sfo", ".")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PopID != "sfo" || plan.Domain != "sfo.example.com" {
		t.Fatalf("%+v", plan)
	}
	if plan.KernelDBNodeID != "udb-sfo" {
		t.Fatalf("node %s", plan.KernelDBNodeID)
	}
	if !strings.Contains(plan.KernelDBPeers, "udb-ams=https://kernel-db.ams.example.com:8480") {
		t.Fatalf("expected ams peer, got %q", plan.KernelDBPeers)
	}
	if strings.Contains(plan.KernelDBPeers, "udb-sfo=") {
		t.Fatalf("should not peer with self: %q", plan.KernelDBPeers)
	}
	// Stack injection
	var udb *BargeProductConfig
	for i := range plan.Stack.Barges {
		if plan.Stack.Barges[i].Name == "ultimate_db" {
			udb = &plan.Stack.Barges[i]
		}
	}
	if udb == nil || udb.NodeID != "udb-sfo" || !strings.Contains(udb.Peers, "udb-ams=") {
		t.Fatalf("stack inject: %+v", udb)
	}
	if plan.SuggestedMode != "stack" {
		t.Fatalf("mode %s", plan.SuggestedMode)
	}
}

func TestExpandSite_HubSpoke(t *testing.T) {
	cfg := &SiteConfig{
		KernelMesh: &KernelMeshPolicy{Mode: "hub-spoke", HubPop: "sfo"},
		Pops: []PopConfig{
			{ID: "sfo", Kernel: &PopKernel{UltimateDB: &KernelNode{NodeID: "udb-sfo", URL: "http://sfo:8480"}}},
			{ID: "ams", Kernel: &PopKernel{UltimateDB: &KernelNode{NodeID: "udb-ams", URL: "http://ams:8480"}}},
			{ID: "nrt", Kernel: &PopKernel{UltimateDB: &KernelNode{NodeID: "udb-nrt", URL: "http://nrt:8480"}}},
		},
	}
	// Spoke only sees hub.
	ams, err := ExpandSite(cfg, "ams", ".")
	if err != nil {
		t.Fatal(err)
	}
	if ams.KernelDBPeers != "udb-sfo=http://sfo:8480" {
		t.Fatalf("spoke peers: %q", ams.KernelDBPeers)
	}
	// Hub sees all spokes.
	sfo, err := ExpandSite(cfg, "sfo", ".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sfo.KernelDBPeers, "udb-ams=") || !strings.Contains(sfo.KernelDBPeers, "udb-nrt=") {
		t.Fatalf("hub peers: %q", sfo.KernelDBPeers)
	}
	if strings.Contains(sfo.KernelDBPeers, "udb-sfo=") {
		t.Fatalf("hub should not include self: %q", sfo.KernelDBPeers)
	}
}

func TestTugconf_SetPopAndKernelMesh(t *testing.T) {
	src := `
# multi-pop site
set site name global-prod
set site domain example.com
set site public_scheme https
set kernel_mesh mode full-mesh
set pop sfo domain sfo.example.com
set pop sfo roles [stack,kernel,anycast]
set pop sfo kernel ultimate_db node_id udb-sfo
set pop sfo kernel ultimate_db url https://kernel-db.sfo.example.com:8480
set pop ams domain ams.example.com
set pop ams roles stack
set pop ams roles kernel
set pop ams kernel ultimate_db node_id udb-ams
set pop ams kernel ultimate_db url https://kernel-db.ams.example.com:8480
set stack barge ultimate_db name ultimate_db
set stack barge williwaw name williwaw
set stack barge williwaw replicas 2
`
	cfg, err := parseTugconf(src, ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Site.Name != "global-prod" || cfg.Site.Domain != "example.com" {
		t.Fatalf("site meta: %+v", cfg.Site)
	}
	if cfg.KernelMesh == nil || cfg.KernelMesh.Mode != "full-mesh" {
		t.Fatalf("mesh: %+v", cfg.KernelMesh)
	}
	if len(cfg.Pops) != 2 {
		t.Fatalf("pops: %+v", cfg.Pops)
	}
	plan, err := ExpandSite(cfg, "sfo", ".")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.KernelDBPeers, "udb-ams=") {
		t.Fatalf("peers from tugconf: %q", plan.KernelDBPeers)
	}
}

func TestLoadSiteAny_YAMLAndTug(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "site.yaml")
	body := `
apiVersion: tunneltug/v1
kind: Site
site:
  name: t
  domain: example.com
kernel_mesh:
  mode: full-mesh
pops:
  - id: a
    kernel:
      ultimate_db:
        node_id: udb-a
        url: http://a:8480
  - id: b
    kernel:
      ultimate_db:
        node_id: udb-b
        url: http://b:8480
stack:
  barges:
    - name: ultimate_db
`
	if err := os.WriteFile(yamlPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadSiteAny(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ExpandSite(cfg, "a", dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.KernelDBPeers != "udb-b=http://b:8480" {
		t.Fatalf("%q", plan.KernelDBPeers)
	}

	tugPath := filepath.Join(dir, "site.tug")
	tug := `
set site name from-tug
set kernel_mesh mode full-mesh
set pop a kernel ultimate_db node_id udb-a
set pop a kernel ultimate_db url http://a:8480
set pop b kernel ultimate_db node_id udb-b
set pop b kernel ultimate_db url http://b:8480
`
	if err := os.WriteFile(tugPath, []byte(tug), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := loadSiteAny(tugPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Site.Name != "from-tug" {
		t.Fatalf("%+v", cfg2.Site)
	}
}

func TestSiteConfigToSetLines_RoundTripSmoke(t *testing.T) {
	cfg := &SiteConfig{
		Site: SiteMeta{Name: "x", Domain: "example.com"},
		Pops: []PopConfig{{ID: "sfo", Domain: "sfo.example.com", Roles: []string{"stack"}}},
	}
	lines, err := SiteConfigToSetLines(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lines, "set ") {
		t.Fatalf("expected set lines: %s", lines)
	}
	// Reload via tugconf
	cfg2, err := parseTugconf(lines, ".")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Site.Domain != "example.com" {
		t.Fatalf("%+v", cfg2.Site)
	}
}

func TestManualKernelPeersNotOverwritten(t *testing.T) {
	cfg := &SiteConfig{
		KernelMesh: &KernelMeshPolicy{Mode: "full-mesh"},
		Pops: []PopConfig{
			{
				ID: "sfo",
				Kernel: &PopKernel{
					UltimateDB: &KernelNode{
						NodeID: "udb-sfo",
						URL:    "http://sfo:8480",
						Peers:  "custom=http://custom:9",
					},
				},
			},
			{
				ID: "ams",
				Kernel: &PopKernel{
					UltimateDB: &KernelNode{NodeID: "udb-ams", URL: "http://ams:8480"},
				},
			},
		},
	}
	plan, err := ExpandSite(cfg, "sfo", ".")
	if err != nil {
		t.Fatal(err)
	}
	if plan.KernelDBPeers != "custom=http://custom:9" {
		t.Fatalf("manual peers should win: %q", plan.KernelDBPeers)
	}
}
