package main

import (
	"strings"
	"testing"
)

func TestBuildBargeStatefulSet(t *testing.T) {
	cfg := k3sBargeConfig{
		Namespace:       "tunneltug",
		Name:            "tunneltug-barge",
		Image:           "tunneltug:test",
		Replicas:        2,
		HostNetwork:     true,
		UpdatePartition: 0,
		ControlBase:     "9001",
		PublicBase:      "8445",
		PortStep:        1,
		Token:           "super-secret-token",
		Domain:          "tunneltug.com",
		LBAddr:          "10.0.0.1:8444",
		FleetID:         "edge1",
		NamespaceLogic:  "default",
		BackendInsecure: true,
		HTTP3:           false,
		SnapshotDir:     "/var/lib/tunneltug/snapshots",
	}

	sts := buildBargeStatefulSet(cfg)
	if sts.Name != "tunneltug-barge" || sts.Namespace != "tunneltug" {
		t.Fatalf("unexpected metadata: %s/%s", sts.Namespace, sts.Name)
	}
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 2 {
		t.Fatalf("replicas: %v", sts.Spec.Replicas)
	}
	if !sts.Spec.Template.Spec.HostNetwork {
		t.Fatal("expected hostNetwork")
	}
	if sts.Spec.UpdateStrategy.RollingUpdate == nil || sts.Spec.UpdateStrategy.RollingUpdate.Partition == nil {
		t.Fatal("expected rolling update partition")
	}
	c := sts.Spec.Template.Spec.Containers[0]
	if c.Image != "tunneltug:test" {
		t.Fatalf("image %s", c.Image)
	}
	joined := strings.Join(c.Args, " ")
	for _, want := range []string{
		"-mode server",
		"-control 9001",
		"-public 8445",
		"-index-from-hostname",
		"-register-lb 10.0.0.1:8444",
		"-backend-insecure",
		"-http3=false",
		"-snapshot-dir /var/lib/tunneltug/snapshots",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	if len(c.VolumeMounts) == 0 || c.VolumeMounts[0].MountPath != "/var/lib/tunneltug/snapshots" {
		t.Fatalf("expected snapshot volume mount, got %#v", c.VolumeMounts)
	}
	foundHostEnv := false
	for _, e := range c.Env {
		if e.Name == "TUNNELTUG_REGISTER_HOST" && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			if e.ValueFrom.FieldRef.FieldPath == "status.hostIP" {
				foundHostEnv = true
			}
		}
	}
	if !foundHostEnv {
		t.Fatal("expected TUNNELTUG_REGISTER_HOST from status.hostIP")
	}
}

func TestParseNodeSelector(t *testing.T) {
	m, err := parseNodeSelector("disk=ssd,role=barge")
	if err != nil {
		t.Fatal(err)
	}
	if m["disk"] != "ssd" || m["role"] != "barge" {
		t.Fatalf("got %#v", m)
	}
	if _, err := parseNodeSelector("bad"); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateConfig_AcceptsK3sRuntime(t *testing.T) {
	resetBargeFlags(t)
	*bargeRuntime = "k3s"
	*k3sImage = "tunneltug:dev"
	*bargeService = "server"
	*controlPort = "9001"
	*publicPort = "8445"
	*bargeReplicas = 2
	if err := validateConfig(); err != nil {
		t.Fatalf("expected valid k3s barge config: %v", err)
	}
}

func TestValidateConfig_RejectsK3sWithoutImage(t *testing.T) {
	resetBargeFlags(t)
	*bargeRuntime = "k3s"
	*k3sImage = ""
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "k3s-image") {
		t.Fatalf("expected k3s-image error, got %v", err)
	}
}

func TestValidateConfig_RejectsK3sClientService(t *testing.T) {
	resetBargeFlags(t)
	*bargeRuntime = "k3s"
	*k3sImage = "tunneltug:dev"
	*bargeService = "client"
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "server") {
		t.Fatalf("expected server-only error, got %v", err)
	}
}

func TestLBEndpointRegisterRequest(t *testing.T) {
	resetBargeFlags(t)
	*authToken = "tokentokentoken"
	*namespace = "ns1"
	reg, err := newBargeLBRegistrar("127.0.0.1:8444")
	if err != nil {
		t.Fatal(err)
	}
	req := reg.registerRequest(lbEndpoint{
		Host:        "10.0.0.5",
		ControlPort: "9001",
		PublicPort:  "8445",
		Namespace:   "ns1",
		FleetID:     "pod-0",
	})
	if req.Host != "10.0.0.5" || req.ControlPort != "9001" || req.PublicPort != "8445" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.FleetID != "pod-0" || req.Namespace != "ns1" {
		t.Fatalf("unexpected fleet/ns: %+v", req)
	}
}
