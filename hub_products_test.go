package main

import (
	"strings"
	"testing"
)

func TestParseHubProductList(t *testing.T) {
	all, err := parseHubProductList("all")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 26 {
		t.Fatalf("all (%d): %v", len(all), all)
	}
	joinedAll := strings.Join(all, ",")
	if !strings.Contains(joinedAll, "ultimate_db") || !strings.Contains(joinedAll, "ultimate_keystore") {
		t.Fatalf("kernel barges missing from all: %v", all)
	}
	list, err := parseHubProductList("mail, cdn, barge")
	if err != nil {
		t.Fatal(err)
	}
	// cdn → social; barge → tunneltug (engine for k3s fleets, not a product)
	if len(list) != 3 {
		t.Fatalf("got %v", list)
	}
	joined := strings.Join(list, ",")
	if !strings.Contains(joined, "social") || !strings.Contains(joined, "mail") || !strings.Contains(joined, "tunneltug") {
		t.Fatalf("unexpected %v", list)
	}
	if _, err := parseHubProductList("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestHubImageRef(t *testing.T) {
	got := hubImageRef("https://hub.tunneltug.com", "0trust/mail", "dev")
	if got != "hub.tunneltug.com/0trust/mail:dev" {
		t.Fatalf("got %s", got)
	}
	got = hubImageRef("hub.tunneltug.com", "tunneltug/engine", "")
	if got != "hub.tunneltug.com/tunneltug/engine:latest" {
		t.Fatalf("got %s", got)
	}
}

func TestResolveHubProduct(t *testing.T) {
	p, err := resolveHubProduct("CDN")
	if err != nil || p.Name != "social" {
		t.Fatalf("%v %+v", err, p)
	}
	p, err = resolveHubProduct("orchid_sync_ingest")
	if err != nil || p.Name != "orchid_ingest" || p.HubRepo != "0trust/orchid-ingest" {
		t.Fatalf("orchid alias: %v %+v", err, p)
	}
	p, err = resolveHubProduct("ztna")
	if err != nil || p.Name != "access" || p.HubRepo != "0trust/access" {
		t.Fatalf("access/ztna alias: %v %+v", err, p)
	}
	p, err = resolveHubProduct("idp")
	if err != nil || p.Name != "auth" {
		t.Fatalf("auth/idp alias: %v %+v", err, p)
	}
	p, err = resolveHubProduct("ultimate-db")
	if err != nil || p.Name != "ultimate_db" || p.HubRepo != "0trust/ultimate-db" {
		t.Fatalf("ultimate_db: %v %+v", err, p)
	}
	p, err = resolveHubProduct("kernel-keystore")
	if err != nil || p.Name != "ultimate_keystore" || p.HubRepo != "0trust/ultimate-keystore" {
		t.Fatalf("ultimate_keystore: %v %+v", err, p)
	}
}
