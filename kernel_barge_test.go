package main

import (
	"testing"
)

func TestSplitKernelPeers(t *testing.T) {
	peers := splitKernelPeers("a=http://udb:8480,b=host:9")
	if len(peers) != 2 || peers[0].id != "a" || peers[1].addr != "host:9" {
		t.Fatalf("%+v", peers)
	}
	bare := splitKernelPeers("http://only:1")
	if len(bare) != 1 || bare[0].id != "peer-1" {
		t.Fatalf("%+v", bare)
	}
}

func TestStackKernelEnv(t *testing.T) {
	env := stackKernelEnv("0trust-stack")
	if env["ULTIMATE_DB_URL"] != "http://ultimate-db.0trust-stack.svc:8480" {
		t.Fatal(env["ULTIMATE_DB_URL"])
	}
	if env["ULTIMATE_KEYSTORE_URL"] != "http://ultimate-keystore.0trust-stack.svc:8481" {
		t.Fatal(env["ULTIMATE_KEYSTORE_URL"])
	}
}

func TestParseStackProducts_KernelBarges(t *testing.T) {
	apps, err := parseStackProducts("ultimate_db,ultimate-keystore,udb")
	if err != nil {
		t.Fatal(err)
	}
	// udb aliases to ultimate_db — two unique deployments
	if len(apps) != 2 {
		t.Fatalf("got %d: %+v", len(apps), apps)
	}
	names := map[string]bool{}
	for _, a := range apps {
		names[a.Name] = true
	}
	if !names["ultimate-db"] || !names["ultimate-keystore"] {
		t.Fatalf("%v", names)
	}
}

func TestStackCatalog_KernelBarges(t *testing.T) {
	cat := stackCatalog()
	db := cat["ultimate_db"]
	if db.Name != "ultimate-db" || db.Port != 8480 || db.Repo != "0trust/ultimate-db" {
		t.Fatalf("%+v", db)
	}
	ks := cat["ultimate_keystore"]
	if ks.Name != "ultimate-keystore" || ks.Port != 8481 {
		t.Fatalf("%+v", ks)
	}
}

func TestHubProducts_KernelBarges(t *testing.T) {
	p, err := resolveHubProduct("kernel-db")
	if err != nil || p.Name != "ultimate_db" || p.HubRepo != "0trust/ultimate-db" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = resolveHubProduct("keystore")
	if err != nil || p.Name != "ultimate_keystore" {
		t.Fatalf("%+v %v", p, err)
	}
}
