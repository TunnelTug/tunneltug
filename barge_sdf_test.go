package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBargeSDF_SignFleetAndImage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	sdf, err := newBargeSDF(filepath.Join(dir, "sdf"))
	if err != nil {
		t.Fatalf("newBargeSDF: %v", err)
	}
	defer sdf.Close()

	cfg := k3sBargeConfig{
		Namespace:   "tunneltug",
		Name:        "tunneltug-barge",
		Image:       "hub.tunneltug.com/tunneltug/engine:dev",
		Replicas:    2,
		ControlBase: "9001",
		PublicBase:  "8445",
		PortStep:    1,
		Domain:      "tunneltug.com",
		LBAddr:      "10.0.0.1:8444",
		FleetID:     "edge1",
		HostNetwork: true,
	}
	// Simulate containerd-resolved digest (no live k3s required).
	att := imageAttestation{
		Reference: cfg.Image,
		Digest:    "sha256:" + strings.Repeat("ab", 32),
		Pinned:    "hub.tunneltug.com/tunneltug/engine@sha256:" + strings.Repeat("ab", 32),
	}
	tok, root, err := sdf.SignFleetReconcileWithImage(context.Background(), cfg, att)
	if err != nil {
		t.Fatalf("SignFleetReconcileWithImage: %v", err)
	}
	if tok == "" || root == "" {
		t.Fatal("expected token and state root")
	}
	if sdf.LastDigest() != att.Digest {
		t.Fatalf("digest not stored: %s", sdf.LastDigest())
	}
	// Token claims must carry both fleet and image fields.
	if extractJWTClaimNested(tok, "image_digest") != att.Digest {
		t.Fatalf("token missing image_digest claim, got %q", extractJWTClaimNested(tok, "image_digest"))
	}
	if extractJWTClaimNested(tok, "replicas") == "" && extractJWTClaimNested(tok, "namespace") == "" {
		// replicas may be number in state_updates — check nested map via payload
		m := jwtPayloadMap(tok)
		su, _ := m["state_updates"].(map[string]interface{})
		if su["namespace"] != cfg.Namespace {
			t.Fatalf("fleet shape not in token: %#v", su)
		}
		if su["image_digest"] != att.Digest {
			t.Fatalf("image not bound: %#v", su)
		}
	}
	st := sdf.Status()
	if st["enabled"] != true || st["format"] != "secure_data_format" {
		t.Fatalf("status: %#v", st)
	}
	binds, _ := st["binds"].([]string)
	if len(binds) < 2 {
		t.Fatalf("expected fleet+image binds: %#v", st["binds"])
	}
	if _, err := os.Stat(filepath.Join(dir, "sdf", "sdf-signing.pem")); err != nil {
		t.Fatalf("signing key: %v", err)
	}
	cm := buildBargeConfigMapSDF(cfg, tok, root, att.Digest)
	if cm.Data["sdf_manifest"] != tok {
		t.Fatal("configmap missing sdf_manifest")
	}
	if cm.Data["image_digest"] != att.Digest {
		t.Fatal("configmap missing image_digest")
	}
	if cm.Annotations["tunneltug.io/integrity"] != "image+fleet" {
		t.Fatal("missing integrity annotation")
	}
}

func TestBargeSDF_RequiresDigest(t *testing.T) {
	dir := t.TempDir()
	sdf, err := newBargeSDF(filepath.Join(dir, "sdf"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdf.Close()
	cfg := k3sBargeConfig{Namespace: "ns", Name: "n", Image: "example.com/app:tag", Replicas: 1}
	// No digest and no containerd → must fail (cannot prove integrity).
	_, _, err = sdf.SignFleetReconcileWithImage(context.Background(), cfg, imageAttestation{Reference: cfg.Image})
	if err == nil {
		t.Fatal("expected error without digest")
	}
}

func TestExtractJWTClaim(t *testing.T) {
	if extractJWTClaim("not-a-jwt", "jti") != "" {
		t.Fatal("expected empty")
	}
}
