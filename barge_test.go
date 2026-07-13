package main

import (
	"flag"
	"strings"
	"testing"
)

func resetBargeFlags(t *testing.T) {
	t.Helper()
	resetFlags(t)
	*mode = "barge"
	*bargeService = "server"
	*bargeReplicas = 2
	*bargePortStep = 10
	*bargeHost = "10.0.0.5"
	*bargeRestartDelay = 5
	*bargeMaxRestarts = 0
	*bargeDashPort = "4050"
	*bargeBufferScale = 1
	*bargeStreamScale = 1
	*bargeLB = ""
	*bargeLBHeartbeat = 10
	*bargeFleetID = ""
	// Process suite: development runtime (production default is k3s).
	*bargeRuntime = "process"
	*k3sImage = ""
	*k3sNamespace = "tunneltug"
	*k3sName = "tunneltug-barge"
	*k3sHostNetwork = true
	*k3sUpdatePartition = 0
	*k3sCleanup = false
	*k3sKubeconfig = ""
	*k3sNodeSelector = ""
	*registerLB = ""
	*registerHost = ""
	*registerFleetID = ""
	*indexFromHostname = false
}

func TestBargeRuntimeDefaultIsK3s(t *testing.T) {
	t.Helper()
	// flag.String DefValue is the production default; package vars are mutated by other tests.
	f := flag.Lookup("barge-runtime")
	if f == nil {
		t.Fatal("missing -barge-runtime flag")
	}
	if f.DefValue != "k3s" {
		t.Fatalf("default barge runtime = %q, want k3s", f.DefValue)
	}
}

func TestValidateConfig_AcceptsBargeMode(t *testing.T) {
	resetBargeFlags(t)
	if err := validateConfig(); err != nil {
		t.Fatalf("expected valid barge config, got %v", err)
	}
}

func TestValidateConfig_RejectsInvalidBargeService(t *testing.T) {
	resetBargeFlags(t)
	*bargeService = "proxy"
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "barge-service") {
		t.Fatalf("expected barge-service error, got %v", err)
	}
}

func TestNewBargeFleet_ServerPortAllocation(t *testing.T) {
	resetBargeFlags(t)
	*controlPort = "9000"
	*publicPort = "8080"
	*bargeReplicas = 3
	*bargePortStep = 2

	fleet, err := newBargeFleet()
	if err != nil {
		t.Fatalf("newBargeFleet: %v", err)
	}
	if len(fleet.barges) != 3 {
		t.Fatalf("expected 3 barges, got %d", len(fleet.barges))
	}
	if fleet.barges[0].controlPort != "9000" || fleet.barges[0].publicPort != "8080" {
		t.Fatalf("unexpected first barge ports: %+v", fleet.barges[0])
	}
	if fleet.barges[2].controlPort != "9004" || fleet.barges[2].publicPort != "8084" {
		t.Fatalf("unexpected third barge ports: %+v", fleet.barges[2])
	}
}

func TestNewBargeFleet_BackendSpec(t *testing.T) {
	resetBargeFlags(t)
	*controlPort = "9000"
	*publicPort = "8080"
	*bargeReplicas = 2
	*bargePortStep = 1
	*bargeHost = "192.168.1.10"

	fleet, err := newBargeFleet()
	if err != nil {
		t.Fatalf("newBargeFleet: %v", err)
	}
	spec := fleet.backendSpec()
	want := "192.168.1.10:9000:8080,192.168.1.10:9001:8081"
	if spec != want {
		t.Fatalf("backendSpec = %q, want %q", spec, want)
	}
}

func TestNewBargeFleet_ClientDashPorts(t *testing.T) {
	resetBargeFlags(t)
	*bargeService = "client"
	*dashPort = "4040"
	*bargeReplicas = 2
	*bargePortStep = 5

	fleet, err := newBargeFleet()
	if err != nil {
		t.Fatalf("newBargeFleet: %v", err)
	}
	if fleet.backendSpec() != "" {
		t.Fatalf("client fleet should not emit backend spec")
	}
	if fleet.barges[1].dashPort != "4045" {
		t.Fatalf("expected dash 4045, got %s", fleet.barges[1].dashPort)
	}
}

func TestNewBargeFleet_RejectsPortOverflow(t *testing.T) {
	resetBargeFlags(t)
	*controlPort = "65530"
	*bargeReplicas = 10
	*bargePortStep = 1

	if _, err := newBargeFleet(); err == nil || !strings.Contains(err.Error(), "65535") {
		t.Fatalf("expected port overflow error, got %v", err)
	}
}

func TestBargeFleet_BuildChildArgs(t *testing.T) {
	resetBargeFlags(t)
	*controlPort = "9000"
	*publicPort = "8080"
	*maxStreams = 100
	*streamBuffer = 131072
	*domain = "example.com"
	*dev = true

	fleet, err := newBargeFleet()
	if err != nil {
		t.Fatalf("newBargeFleet: %v", err)
	}

	args := fleet.buildChildArgs(fleet.barges[0])
	joined := strings.Join(args, " ")
	for _, want := range []string{"-mode server", "-control 9000", "-public 8080", "-maxstreams 100", "-buffer 131072", "-domain example.com", "-dev"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected child args to contain %q, got: %s", want, joined)
		}
	}
}

func TestValidateConfig_AcceptsBargeWithLBRegistration(t *testing.T) {
	resetBargeFlags(t)
	*bargeLB = "127.0.0.1:8443"
	*dev = true
	*domain = "localhost"
	if err := validateConfig(); err != nil {
		t.Fatalf("expected valid barge lb config, got %v", err)
	}
}

func TestValidateConfig_RejectsBargeLBForClientService(t *testing.T) {
	resetBargeFlags(t)
	*bargeService = "client"
	*bargeLB = "127.0.0.1:8443"
	if err := validateConfig(); err == nil || !strings.Contains(err.Error(), "barge-lb") {
		t.Fatalf("expected barge-lb/server error, got %v", err)
	}
}

func TestNewBargeLBRegistrar_BuildsHTTPSURL(t *testing.T) {
	resetBargeFlags(t)
	*dev = true
	*domain = "localhost"
	*bargeFleetID = "prod"

	reg, err := newBargeLBRegistrar("lb.example.com:443")
	if err != nil {
		t.Fatalf("newBargeLBRegistrar: %v", err)
	}
	if reg.baseURL != "https://lb.example.com:443" {
		t.Fatalf("unexpected base URL: %s", reg.baseURL)
	}
	if reg.fleetLabel(&bargeInstance{id: 2}) != "prod-2" {
		t.Fatalf("unexpected fleet label")
	}
}

func TestBargeScalingProfile(t *testing.T) {
	resetBargeFlags(t)
	*streamBuffer = 131072
	*maxStreams = 50
	*bargeBufferScale = 2
	*bargeStreamScale = 3

	bargeScalingProfile()

	if *streamBuffer != 262144 {
		t.Fatalf("buffer scale: got %d, want 262144", *streamBuffer)
	}
	if *maxStreams != 150 {
		t.Fatalf("stream scale: got %d, want 150", *maxStreams)
	}
}