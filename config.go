package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const defaultWeakToken = "secret123"

var subdomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func applyEnvDefaults() {
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_CONFIG")); v != "" && strings.TrimSpace(*siteConfigFlag) == "" {
		*siteConfigFlag = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_POP")); v != "" && strings.TrimSpace(*sitePopFlag) == "" {
		*sitePopFlag = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_TOKEN")); v != "" {
		// Only accept cryptographically strong env tokens; weak values are ignored
		// so ensureAuthToken can mint a secure one (or fail in -prod).
		if !isWeakToken(v) {
			*authToken = v
		} else {
			log.Printf("WARNING: ignoring weak TUNNELTUG_TOKEN; will require -token or auto-mint")
			*authToken = ""
		}
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_HUB_LISTEN")); v != "" && strings.TrimSpace(*hubListen) == ":5000" {
		*hubListen = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_HUB_PUBLIC")); v != "" && strings.TrimSpace(*hubPublic) == "" {
		*hubPublic = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_HUB_S3_URL")); v != "" && strings.TrimSpace(*hubS3URL) == "https://0trust.social" {
		*hubS3URL = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_HUB_BUCKET")); v != "" && strings.TrimSpace(*hubBucket) == "tunneltug-hub" {
		*hubBucket = v
	}
	if envTruthy("TUNNELTUG_HUB_MEMORY") {
		*hubMemory = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_HUB_PRODUCTS")); v != "" {
		*hubProducts = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_HUB_TAG")); v != "" {
		*hubTag = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_HUB_HOST")); v != "" {
		*hubHost = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_STACK_PRODUCTS")); v != "" {
		*stackProducts = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_STACK_NAMESPACE")); v != "" {
		*stackNamespace = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_STACK_TAG")); v != "" {
		*stackTag = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_STACK_CONFIG")); v != "" && strings.TrimSpace(*stackConfig) == "" {
		*stackConfig = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_BARGE_CONFIG")); v != "" && strings.TrimSpace(*bargeConfig) == "" {
		*bargeConfig = v
	}
	if envTruthy("TUNNELTUG_K3S_STACK") {
		*k3sStack = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_DOMAIN")); v != "" && strings.TrimSpace(*domain) == "" {
		*domain = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_SERVER")); v != "" && strings.TrimSpace(*serverIP) == "127.0.0.1" {
		*serverIP = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_SUBDOMAIN")); v != "" && strings.TrimSpace(*subdomain) == "myapp" {
		*subdomain = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_BACKENDS")); v != "" && strings.TrimSpace(*lbBackends) == "" {
		*lbBackends = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_BARGE_LB")); v != "" && strings.TrimSpace(*bargeLB) == "" {
		*bargeLB = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_BARGE_FLEET_ID")); v != "" && strings.TrimSpace(*bargeFleetID) == "" {
		*bargeFleetID = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_BARGE_RUNTIME")); v != "" && strings.TrimSpace(*bargeRuntime) == "k3s" {
		*bargeRuntime = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_K3S_KUBECONFIG")); v != "" && strings.TrimSpace(*k3sKubeconfig) == "" {
		*k3sKubeconfig = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_K3S_NAMESPACE")); v != "" && strings.TrimSpace(*k3sNamespace) == "tunneltug" {
		*k3sNamespace = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_K3S_IMAGE")); v != "" && strings.TrimSpace(*k3sImage) == "" {
		*k3sImage = v
	}
	// Default barge image from the k3s-layer hub when unset.
	if strings.TrimSpace(*k3sImage) == "" {
		*k3sImage = defaultK3sBargeImage
	}
	if envTruthy("TUNNELTUG_K3S_HUB") {
		*k3sHub = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_K3S_HUB")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "no", "off":
			*k3sHub = false
		}
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_K3S_HUB_PULL")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "no", "off":
			*k3sHubPull = false
		case "1", "true", "yes", "on":
			*k3sHubPull = true
		}
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_K3S_NAME")); v != "" && strings.TrimSpace(*k3sName) == "tunneltug-barge" {
		*k3sName = v
	}
	if envTruthy("TUNNELTUG_K3S_CLEANUP") {
		*k3sCleanup = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_K3S_NODE_SELECTOR")); v != "" && strings.TrimSpace(*k3sNodeSelector) == "" {
		*k3sNodeSelector = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_REGISTER_LB")); v != "" && strings.TrimSpace(*registerLB) == "" {
		*registerLB = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_REGISTER_HOST")); v != "" && strings.TrimSpace(*registerHost) == "" {
		*registerHost = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_REGISTER_FLEET_ID")); v != "" && strings.TrimSpace(*registerFleetID) == "" {
		*registerFleetID = v
	}
	if envTruthy("TUNNELTUG_INDEX_FROM_HOSTNAME") {
		*indexFromHostname = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_SNAPSHOT_DIR")); v != "" && strings.TrimSpace(*snapshotDir) == "" {
		*snapshotDir = v
	}
	if envTruthy("TUNNELTUG_SNAPSHOT_RESTORE") {
		*snapshotRestore = true
	}
	if envTruthy("TUNNELTUG_SNAPSHOT_ON_SHUTDOWN") {
		*snapshotOnShutdown = true
	}
	if envTruthy("TUNNELTUG_MESH") {
		*meshEnabled = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_DNS")); v != "" {
		*meshDNS = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_TLD")); v != "" {
		*meshTLD = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_ZONE")); v != "" {
		*meshZone = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_NS")); v != "" {
		*meshNSHost = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_EDGE_IP")); v != "" {
		*meshEdgeIP = v
	}
	if envTruthy("TUNNELTUG_MESH_JOIN_PLATFORM") {
		*meshJoinPlatform = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_PLATFORM")); v != "" {
		*meshPlatform = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_GATEWAY")); v != "" {
		*meshGateway = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_PUBKEY")); v != "" {
		*meshPubkey = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_HOST")); v != "" {
		*meshHost = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_REGISTER_URL")); v != "" {
		*meshRegisterURL = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_MESH_DATA_DIR")); v != "" {
		*meshDataDir = v
	}
	if envTruthy("TUNNELTUG_VPI_STUB") || envTruthy("TUNNELTUG_MESH") {
		*vpiStub = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_VPI_UPSTREAM")); v != "" {
		*vpiUpstream = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_VPI_LISTEN")); v != "" {
		*vpiListen = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_VHOSTS")); v != "" && strings.TrimSpace(*vhostsFile) == "" {
		*vhostsFile = v
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_DNS")); v != "" && strings.TrimSpace(*dnsFileFlag) == "" {
		*dnsFileFlag = v
	}
	// Custom DNS zones imply a local stub resolver.
	if dnsConfigActive() {
		*vpiStub = true
	}
	if envTruthy("TUNNELTUG_ANYCAST") {
		*anycastEnable = true
	}
	if v := strings.TrimSpace(os.Getenv("TUNNELTUG_ANYCAST_CONFIG")); v != "" && strings.TrimSpace(*anycastConfig) == "" {
		*anycastConfig = v
	}
	// Standalone anycast mode implies the feature is on.
	if strings.ToLower(strings.TrimSpace(*mode)) == "anycast" {
		*anycastEnable = true
	}
}

func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func validateConfig() error {
	modeVal := strings.ToLower(strings.TrimSpace(*mode))
	if modeVal != "server" && modeVal != "client" && modeVal != "lb" && modeVal != "barge" && modeVal != "orchestrator" && modeVal != "anycast" && modeVal != "hub" && modeVal != "hub-publish" && modeVal != "stack" && modeVal != "ultimate_db" && modeVal != "ultimate-db" && modeVal != "ultimate_keystore" && modeVal != "ultimate-keystore" {
		return fmt.Errorf("invalid -mode %q: use server, client, lb, barge, orchestrator, anycast, hub, hub-publish, stack, ultimate_db, or ultimate_keystore", *mode)
	}

	// Standalone anycast edge: only validate anycast YAML (no tunnel token/ports).
	if modeVal == "anycast" {
		if anycastConfigPath() == "" && strings.TrimSpace(*anycastGenBGPsecKey) != "" {
			return nil
		}
		if _, err := loadAnycastConfig(); err != nil {
			return err
		}
		return nil
	}

	// Barge image hub: OCI registry with public pull / auth push (token validated separately).
	if modeVal == "hub" {
		if err := ensureAuthToken(); err != nil {
			return err
		}
		if strings.TrimSpace(*hubBucket) == "" {
			return fmt.Errorf("-hub-bucket must not be empty")
		}
		return nil
	}
	// Multi-product push to hub (local k3s images → hub.tunneltug.com/0trust/*).
	if modeVal == "hub-publish" {
		if err := ensureAuthToken(); err != nil {
			return err
		}
		if _, err := parseHubProductList(*hubProducts); err != nil {
			return err
		}
		return nil
	}
	// Dedicated ultimate_db / ultimate_keystore kernel barges (own data, not mesh/SDF).
	if modeVal == "ultimate_db" || modeVal == "ultimate-db" || modeVal == "ultimate_keystore" || modeVal == "ultimate-keystore" {
		if err := ensureAuthToken(); err != nil {
			return err
		}
		return nil
	}

	// Product stack: Deployments/Services via client-go (self-contained, no kubectl).
	if modeVal == "stack" {
		if err := ensureAuthToken(); err != nil {
			return err
		}
		if cfgPath := stackConfigPath(); cfgPath != "" {
			if _, err := loadStackConfig(cfgPath); err != nil {
				return fmt.Errorf("stack-config: %w", err)
			}
		} else if _, err := parseStackProducts(*stackProducts); err != nil {
			return err
		}
		if err := validatePort("stack-dash", *stackDashPort); err != nil {
			return err
		}
		return nil
	}

	// Sidecar anycast on server/lb.
	if *anycastEnable {
		if modeVal != "server" && modeVal != "lb" {
			return fmt.Errorf("-anycast sidecar is only valid with -mode server or lb (use -mode anycast for standalone)")
		}
		if _, err := loadAnycastConfig(); err != nil {
			return err
		}
	}

	routingVal := strings.ToLower(strings.TrimSpace(*routing))
	if routingVal != "subdomain" && routingVal != "direct" {
		return fmt.Errorf("invalid -routing %q: use subdomain or direct", *routing)
	}

	if *prod && *dev {
		return fmt.Errorf("cannot use -prod and -dev together")
	}

	if (*prod || *dev) && strings.TrimSpace(*domain) == "" {
		return fmt.Errorf("-domain is required with -prod or -dev")
	}

	// Cryptographic tokens only — mint if empty (non-prod) or reject weak defaults.
	if err := ensureAuthToken(); err != nil {
		return err
	}

	for _, spec := range []struct {
		name string
		val  *string
	}{
		{"public", publicPort},
		{"control", controlPort},
		{"dash", dashPort},
		{"local", localPort},
	} {
		if err := validatePort(spec.name, *spec.val); err != nil {
			return err
		}
	}

	if err := validateNamespace(*namespace); err != nil {
		return err
	}

	if modeVal == "client" && routingVal == "subdomain" {
		sub := strings.ToLower(strings.TrimSpace(*subdomain))
		if !subdomainPattern.MatchString(sub) {
			return fmt.Errorf("invalid -subdomain %q: use lowercase letters, numbers, and hyphens", *subdomain)
		}
	}

	// Direct + prod is the simple single-tunnel path: apex domain only, no subdomain required.
	if *prod && routingVal == "direct" && modeVal == "client" {
		if strings.TrimSpace(*serverIP) == "" || strings.TrimSpace(*serverIP) == "127.0.0.1" {
			if strings.TrimSpace(*domain) == "" {
				return fmt.Errorf("direct -prod client requires -server or -domain")
			}
		}
	}

	if meshActive() {
		tld := strings.ToLower(strings.TrimSpace(*meshTLD))
		if tld == "" || strings.Contains(tld, ".") {
			return fmt.Errorf("invalid -mesh-tld %q: use a single label (e.g. tunnel)", *meshTLD)
		}
		zone := strings.ToLower(strings.TrimSpace(*meshZone))
		parts := strings.Split(zone, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("invalid -mesh-zone %q: use a root domain under the private TLD (e.g. tunneltug.tunnel)", *meshZone)
		}
		if parts[1] != tld {
			return fmt.Errorf("-mesh-zone %q must end with -mesh-tld .%s", *meshZone, tld)
		}
	}

	if dnsConfigActive() {
		if err := loadDNSConfig(); err != nil {
			return err
		}
	}

	if modeVal == "orchestrator" {
		if _, err := parseStaticBackends(*lbBackends); err != nil {
			return err
		}
		if strings.TrimSpace(*lbBackends) == "" && !*lbDynamic {
			return fmt.Errorf("at least one backend is required (use -backends or -lb-dynamic=true)")
		}
		policy := strings.ToLower(strings.TrimSpace(*lbPolicy))
		if policy != "sticky" && policy != "round-robin" && policy != "rr" {
			return fmt.Errorf("invalid -lb-policy %q: use sticky or round-robin", *lbPolicy)
		}
		if *lbRegisterTTL < 5 {
			return fmt.Errorf("-lb-register-ttl must be at least 5 seconds")
		}
		if err := validatePort("orch-dash", *orchDashPort); err != nil {
			return err
		}
	}

	if *keepAlive < 5 {
		return fmt.Errorf("-keepalive must be at least 5 seconds")
	}

	if *streamBuffer < 32*1024 || *streamBuffer > maxStreamBuffer {
		return fmt.Errorf("-buffer must be between 32KB and %d bytes", maxStreamBuffer)
	}

	if modeVal == "lb" {
		if _, err := parseStaticBackends(*lbBackends); err != nil {
			return err
		}
		if strings.TrimSpace(*lbBackends) == "" && !*lbDynamic {
			return fmt.Errorf("at least one backend is required (use -backends or -lb-dynamic=true)")
		}
		policy := strings.ToLower(strings.TrimSpace(*lbPolicy))
		if policy != "sticky" && policy != "round-robin" && policy != "rr" {
			return fmt.Errorf("invalid -lb-policy %q: use sticky or round-robin", *lbPolicy)
		}
		if *lbRegisterTTL < 5 {
			return fmt.Errorf("-lb-register-ttl must be at least 5 seconds")
		}
	}

	if modeVal == "barge" {
		if err := validateBargeConfig(); err != nil {
			return err
		}
	}

	if modeVal == "server" {
		if err := validateServerRegisterConfig(); err != nil {
			return err
		}
	}

	if *snapshotInterval < 0 {
		return fmt.Errorf("-snapshot-interval must be >= 0")
	}
	if *snapshotKeep < 1 {
		return fmt.Errorf("-snapshot-keep must be at least 1")
	}

	return nil
}

func validateBargeConfig() error {
	service := strings.ToLower(strings.TrimSpace(*bargeService))
	if service != "server" && service != "client" {
		return fmt.Errorf("invalid -barge-service %q: use server or client", *bargeService)
	}
	runtime := bargeRuntimeMode()
	if runtime != "process" && runtime != "k3s" {
		return fmt.Errorf("invalid -barge-runtime %q: use k3s (production) or process (development)", *bargeRuntime)
	}
	if *bargeReplicas < 1 {
		return fmt.Errorf("-barge-replicas must be at least 1")
	}
	if *bargePortStep < 1 {
		return fmt.Errorf("-barge-port-step must be at least 1")
	}
	if *bargeRestartDelay < 1 {
		return fmt.Errorf("-barge-restart-delay must be at least 1 second")
	}
	if *bargeBufferScale < 1 {
		return fmt.Errorf("-barge-buffer-scale must be at least 1")
	}
	if *bargeStreamScale < 1 {
		return fmt.Errorf("-barge-stream-scale must be at least 1")
	}
	if err := validatePort("barge-dash", *bargeDashPort); err != nil {
		return err
	}
	if *bargeLBHeartbeat < 1 {
		return fmt.Errorf("-barge-lb-heartbeat must be at least 1 second")
	}
	if lbAddr := strings.TrimSpace(*bargeLB); lbAddr != "" {
		if service != "server" {
			return fmt.Errorf("-barge-lb requires -barge-service server")
		}
		if !strings.Contains(lbAddr, ":") {
			return fmt.Errorf("invalid -barge-lb %q: use host:port", lbAddr)
		}
	}
	if runtime == "k3s" {
		if service != "server" {
			return fmt.Errorf("-barge-runtime k3s requires -barge-service server")
		}
		// Image defaults to the k3s barge hub ref when unset.
		if strings.TrimSpace(*k3sImage) == "" {
			*k3sImage = defaultK3sBargeImage
		}
		ns := strings.TrimSpace(*k3sNamespace)
		if ns == "" {
			return fmt.Errorf("-k3s-namespace must not be empty")
		}
		name := strings.TrimSpace(*k3sName)
		if name == "" {
			return fmt.Errorf("-k3s-name must not be empty")
		}
		if *k3sUpdatePartition < 0 {
			return fmt.Errorf("-k3s-update-partition must be >= 0")
		}
		if path := strings.TrimSpace(*k3sKubeconfig); path != "" {
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("-k3s-kubeconfig %q: %w", path, err)
			}
		}
		if err := validateBargePortRange(*controlPort, *bargeReplicas, *bargePortStep, "control"); err != nil {
			return err
		}
		if err := validateBargePortRange(*publicPort, *bargeReplicas, *bargePortStep, "public"); err != nil {
			return err
		}
		return nil
	}
	if _, err := newBargeFleet(); err != nil {
		return err
	}
	return nil
}

func validateServerRegisterConfig() error {
	if !serverSelfRegisterActive() {
		return nil
	}
	if !strings.Contains(registerLBAddr(), ":") {
		return fmt.Errorf("invalid -register-lb %q: use host:port", *registerLB)
	}
	host := strings.TrimSpace(*registerHost)
	if host == "" {
		host = strings.TrimSpace(*bargeHost)
	}
	if host == "" {
		return fmt.Errorf("-register-host is required with -register-lb")
	}
	if *bargeLBHeartbeat < 1 {
		return fmt.Errorf("-barge-lb-heartbeat must be at least 1 second")
	}
	return nil
}

func validatePort(name, value string) error {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid -%s port %q", name, value)
	}
	return nil
}

func applyProductionDefaults() {
	if !*prod {
		return
	}
	if *publicPort == "8080" {
		*publicPort = "443"
	}
	if *keepAlive == 30 {
		*keepAlive = 15
	}
	if *streamBuffer == 262144 {
		*streamBuffer = 524288
	}
	// Simple direct production: clients default their control target to the domain
	// when -server is still the local default.
	if isDirectRouting() && strings.ToLower(strings.TrimSpace(*mode)) == "client" {
		if strings.TrimSpace(*serverIP) == "127.0.0.1" && strings.TrimSpace(*domain) != "" {
			*serverIP = strings.TrimSpace(*domain)
		}
	}
}
