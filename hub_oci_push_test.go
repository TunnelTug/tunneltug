package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackBinaryImage(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "product")
	payload := []byte("#!/bin/sh\necho tunneltug-product\n")
	if err := os.WriteFile(bin, payload, 0755); err != nil {
		t.Fatal(err)
	}
	cfg, layer, man, err := packBinaryImage(bin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.digest, "sha256:") || cfg.size == 0 {
		t.Fatalf("bad config blob: %+v", cfg)
	}
	if !strings.HasPrefix(layer.digest, "sha256:") || layer.size == 0 {
		t.Fatalf("bad layer blob: %+v", layer)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(man, &m); err != nil {
		t.Fatal(err)
	}
	if m["schemaVersion"].(float64) != 2 {
		t.Fatalf("manifest: %s", man)
	}
}

func TestPublishBinaryToHubRoundTrip(t *testing.T) {
	h := testHub(t)
	// Wrap real handler
	srv := httptest.NewServer(http.HandlerFunc(h.handleV2))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	bin := filepath.Join(dir, "product")
	if err := os.WriteFile(bin, []byte("binary-bytes-name-product"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg, layer, man, err := packBinaryImage(bin)
	if err != nil {
		t.Fatal(err)
	}
	// Use server URL host with scheme stripped for hubBaseURL path — pushOCIToHub wants full baseURL
	if err := pushOCIToHub(srv.URL, "0trust/name", "test", h.cfg.Token, cfg, layer, man); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Pull manifest publicly
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v2/0trust/name/manifests/test", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest get %d", resp.StatusCode)
	}
	got := new(bytes.Buffer)
	got.ReadFrom(resp.Body)
	if !bytes.Equal(got.Bytes(), man) {
		t.Fatalf("manifest mismatch")
	}

	// Blob digests match
	for _, dig := range []string{cfg.digest, layer.digest} {
		req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v2/0trust/name/blobs/"+dig, nil)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("blob %s → %d", dig, resp.StatusCode)
		}
	}

	// Sanity: layer digest is real sha256
	sum := sha256.Sum256(layer.data)
	if layer.digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("layer digest self-check")
	}
}
