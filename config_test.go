package main

import (
	"strings"
	"testing"
)

func resetFlags(t *testing.T) {
	t.Helper()
	*mode = "server"
	*routing = "subdomain"
	*prod = false
	*dev = false
	*domain = ""
	*authToken = "this-is-a-valid-token"
	*publicPort = "8080"
	*controlPort = "9000"
	*dashPort = "4040"
	*localPort = "3000"
	*subdomain = "myapp"
	*keepAlive = 30
	*streamBuffer = 262144
	*lbBackends = ""
	*lbPolicy = "sticky"
	*backendInsecure = false
	*bargeService = "server"
	*bargeReplicas = 1
	*bargePortStep = 1
	*bargeHost = "127.0.0.1"
	*bargeRestartDelay = 5
	*bargeMaxRestarts = 0
	*bargeDashPort = "4050"
	*bargeBufferScale = 1
	*bargeStreamScale = 1
	*bargeLB = ""
	*bargeLBHeartbeat = 10
	*bargeFleetID = ""
	*lbDynamic = true
	*lbRegisterTTL = 45
	*namespace = ""
	*orchDashPort = "4060"
	*meshEnabled = false
	*meshJoinPlatform = false
	*meshTLD = "tunnel"
	*meshZone = "tunneltug.tunnel"
	*meshDNS = "127.0.0.1:5353"
	*meshNSHost = "ns.tunneltug.tunnel"
	*meshEdgeIP = ""
	*meshDataDir = ""
	*meshHost = ""
	*dnsFileFlag = ""
	*vpiStub = false
	dnsMu.Lock()
	dnsFile = DNSFile{}
	dnsLoaded = false
	dnsMu.Unlock()
}

func TestValidateConfig_RejectsWeakProdToken(t *testing.T) {
	resetFlags(t)
	*prod = true
	*domain = "example.com"
	*authToken = "short"

	err := validateConfig()
	if err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("expected prod token length error, got %v", err)
	}
}

func TestValidateConfig_AcceptsValidServerConfig(t *testing.T) {
	resetFlags(t)
	if err := validateConfig(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestValidateConfig_RejectsInvalidSubdomain(t *testing.T) {
	resetFlags(t)
	*mode = "client"
	*routing = "subdomain"
	*subdomain = "-invalid"

	if err := validateConfig(); err == nil {
		t.Fatal("expected invalid subdomain error")
	}
}

func TestValidatePort(t *testing.T) {
	if err := validatePort("test", "70000"); err == nil {
		t.Fatal("expected invalid port error")
	}
	if err := validatePort("test", "443"); err != nil {
		t.Fatalf("expected valid port, got %v", err)
	}
}
