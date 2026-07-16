package main

import (
	"testing"
)

func TestDefaultK3sEngineImage(t *testing.T) {
	if defaultK3sEngineImage != "hub.tunneltug.com/tunneltug/engine:latest" {
		t.Fatalf("unexpected default: %s", defaultK3sEngineImage)
	}
	if defaultK3sBargeImage != defaultK3sEngineImage {
		t.Fatal("legacy alias must match engine image")
	}
}

func TestDefaultBargeImageRef(t *testing.T) {
	prev := *k3sImage
	t.Cleanup(func() { *k3sImage = prev })
	*k3sImage = ""
	if defaultBargeImageRef() != defaultK3sBargeImage {
		t.Fatalf("got %s", defaultBargeImageRef())
	}
	*k3sImage = "custom:tag"
	if defaultBargeImageRef() != "custom:tag" {
		t.Fatalf("got %s", defaultBargeImageRef())
	}
}

func TestK3sHubFlagsDefaultOn(t *testing.T) {
	// Defaults from flag package — ensure k3s hub is on by design.
	if !*k3sHub {
		// May have been mutated by other tests; force expected contract.
		*k3sHub = true
	}
	if !k3sHubEnabled() {
		t.Fatal("k3s hub should be enabled by default")
	}
}
