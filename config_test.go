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
	// 32+ chars so ensureAuthToken accepts it without auto-mint.
	*authToken = "this-is-a-valid-token-32chars!!"
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
	if err == nil {
		t.Fatal("expected prod token error")
	}
	// Empty/weak tokens fail either as required-in-prod or too-weak.
	if !strings.Contains(err.Error(), "token") && !strings.Contains(err.Error(), "weak") && !strings.Contains(err.Error(), "at least") {
		t.Fatalf("expected token-related error, got %v", err)
	}
}

func TestValidateConfig_RejectsKnownWeakToken(t *testing.T) {
	resetFlags(t)
	*authToken = "secret123"
	err := validateConfig()
	// secret123 is cleared path: empty default weak → auto-mint in non-prod succeeds.
	// Force weak non-empty known default after ensure would reject if not cleared.
	*authToken = "0trust-tunnel-prod-secret"
	err = validateConfig()
	if err == nil {
		t.Fatal("expected known weak token rejection")
	}
}

func TestValidateConfig_HubMode(t *testing.T) {
	resetFlags(t)
	*mode = "hub"
	*authToken = "this-is-a-valid-token-32chars!!"
	*hubBucket = "tunneltug-hub"
	if err := validateConfig(); err != nil {
		t.Fatalf("hub config: %v", err)
	}
}

func TestGenerateSecureToken(t *testing.T) {
	a, err := GenerateSecureToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSecureToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != 64 {
		t.Fatalf("bad tokens a=%q b=%q", a, b)
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
