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
	if len(all) != 9 {
		t.Fatalf("all: %v", all)
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
}
