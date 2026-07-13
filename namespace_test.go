package main

import (
	"testing"
)

func TestComposeTunnelKey(t *testing.T) {
	*routing = "subdomain"
	if got := composeTunnelKey("", "myapp"); got != "myapp" {
		t.Fatalf("default namespace key = %q, want myapp", got)
	}
	if got := composeTunnelKey("prod", "myapp"); got != "prod/myapp" {
		t.Fatalf("namespaced key = %q, want prod/myapp", got)
	}
}

func TestSplitTunnelKey(t *testing.T) {
	ns, sub := splitTunnelKey("prod/myapp")
	if ns != "prod" || sub != "myapp" {
		t.Fatalf("split namespaced = %q/%q", ns, sub)
	}
	ns, sub = splitTunnelKey("myapp")
	if ns != defaultNamespace || sub != "myapp" {
		t.Fatalf("split legacy = %q/%q", ns, sub)
	}
}

func TestTunnelKeyFromHost_NamespaceRouting(t *testing.T) {
	setRoutingFlags(t, "subdomain", "example.com")
	*namespace = ""

	if got := tunnelKeyFromHost("myapp.example.com"); got != "myapp" {
		t.Fatalf("plain host = %q, want myapp", got)
	}
	if got := tunnelKeyFromHost("myapp.prod.example.com"); got != "prod/myapp" {
		t.Fatalf("namespaced host = %q, want prod/myapp", got)
	}
}

func TestClientTunnelKey_WithNamespace(t *testing.T) {
	setRoutingFlags(t, "subdomain", "example.com")
	*subdomain = "api"
	*namespace = "staging"
	if got := clientTunnelKey(); got != "staging/api" {
		t.Fatalf("clientTunnelKey() = %q, want staging/api", got)
	}
}

func TestPublicURL_WithNamespace(t *testing.T) {
	*prod = false
	*dev = false
	*publicPort = "8080"
	*serverIP = "127.0.0.1"
	setRoutingFlags(t, "subdomain", "example.com")
	*subdomain = "myapp"
	*namespace = "prod"

	want := "http://myapp.prod.example.com:8080"
	if got := publicURL(); got != want {
		t.Fatalf("publicURL() = %q, want %q", got, want)
	}
}