package main

import (
	"testing"
)

func setRoutingFlags(t *testing.T, routingMode, domainName string) {
	t.Helper()
	*routing = routingMode
	*domain = domainName
}

func TestTunnelKeyFromHost_SubdomainRouting(t *testing.T) {
	setRoutingFlags(t, "subdomain", "example.com")

	cases := []struct {
		host string
		want string
	}{
		{"myapp.example.com", "myapp"},
		{"api.example.com", "api"},
		{"example.com", ""},
		{"localhost", ""},
		{"127.0.0.1", ""},
		{"nested.sub.example.com", "sub/nested"},
	}

	for _, tc := range cases {
		if got := tunnelKeyFromHost(tc.host); got != tc.want {
			t.Fatalf("tunnelKeyFromHost(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestTunnelKeyFromHost_DirectRouting(t *testing.T) {
	setRoutingFlags(t, "direct", "example.com")

	if got := tunnelKeyFromHost("anything.example.com"); got != defaultTunnelKey {
		t.Fatalf("direct routing host = %q, want %q", got, defaultTunnelKey)
	}
}

func TestClientTunnelKey(t *testing.T) {
	setRoutingFlags(t, "subdomain", "")
	*subdomain = "demo"
	*namespace = ""
	if got := clientTunnelKey(); got != "demo" {
		t.Fatalf("clientTunnelKey() = %q, want demo", got)
	}

	setRoutingFlags(t, "direct", "")
	*namespace = ""
	if got := clientTunnelKey(); got != defaultTunnelKey {
		t.Fatalf("direct clientTunnelKey() = %q, want %q", got, defaultTunnelKey)
	}
}

func TestPublicURL(t *testing.T) {
	*prod = false
	*dev = false
	*publicPort = "8080"
	*serverIP = "127.0.0.1"
	setRoutingFlags(t, "subdomain", "")
	*subdomain = "myapp"

	want := "http://myapp.localhost:8080"
	if got := publicURL(); got != want {
		t.Fatalf("publicURL() = %q, want %q", got, want)
	}
}
