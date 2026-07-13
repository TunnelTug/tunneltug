package main

import (
	"crypto/tls"
	"flag"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/crypto/acme/autocert"
)

const defaultTunnelKey = "default"

var (
	mode         = flag.String("mode", "client", "Run mode: 'server', 'client', 'lb', 'barge', or 'orchestrator'")
	protocol     = flag.String("proto", "quic", "Control transport protocol: 'quic' (UDP)")
	routing      = flag.String("routing", "subdomain", "Routing mode: 'subdomain' (host-based) or 'direct' (single tunnel, no subdomain)")
	serverIP     = flag.String("server", "127.0.0.1", "Server IP (client mode)")
	localPort    = flag.String("local", "3000", "Local port to expose (client mode)")
	publicPort   = flag.String("public", "8080", "Public HTTP port (server mode)")
	controlPort  = flag.String("control", "9000", "Control port (used by both)")
	subdomain    = flag.String("subdomain", "myapp", "Requested subdomain (client mode, subdomain routing only)")
	namespace    = flag.String("namespace", "", "Logical namespace for tunnel routing and orchestration (default: default)")
	authToken    = flag.String("token", "secret123", "Authentication token (or set TUNNELTUG_TOKEN)")
	dashPort     = flag.String("dash", "4040", "Local dashboard port (client mode)")
	prod         = flag.Bool("prod", false, "Production mode: obtain TLS certs via ACME (Let's Encrypt)")
	dev          = flag.Bool("dev", false, "Development mode: generate a self-signed TLS cert for -domain")
	domain       = flag.String("domain", "", "Primary domain name (required with -prod or -dev)")
	subalt       = flag.String("subalt", "", "Comma-separated subject alternative names (e.g. '*.example.com,app.example.com')")
	email        = flag.String("email", "", "Contact email for ACME registration (-prod)")
	acmeCache    = flag.String("acme-cache", "certs-cache", "ACME certificate cache directory (-prod)")
	acmeHTTP     = flag.Bool("acme-http", true, "Listen on :80 for ACME HTTP-01 challenges (-prod)")
	certFile     = flag.String("cert", "", "Path to TLS certificate (server mode). Leaves blank for auto-gen.")
	keyFile      = flag.String("key", "", "Path to TLS key (server mode). Leave blank for auto-gen.")
	insecure     = flag.Bool("insecure", false, "Skip TLS verification (client mode; dev/self-signed only)")
	quiet        = flag.Bool("quiet", false, "Suppress all non-fatal logging")
	keepAlive    = flag.Int("keepalive", 30, "Yamux Keep-Alive interval in seconds")
	streamBuffer = flag.Int("buffer", 262144, "Streaming copy buffer size in bytes (default 256KB)")
	maxStreams   = flag.Int("maxstreams", 0, "Max concurrent streams per tunnel client (0 = unlimited)")
	http3Enabled = flag.Bool("http3", true, "Enable HTTP/3 (QUIC) on the public ingress when TLS is enabled")
	showVersion      = flag.Bool("version", false, "Print version and exit")
	lbBackends       = flag.String("backends", "", "Comma-separated backend list: host[:control[:public]] (lb mode)")
	lbPolicy         = flag.String("lb-policy", "sticky", "LB assignment policy: sticky (least-loaded) or round-robin (lb mode)")
	backendInsecure  = flag.Bool("backend-insecure", false, "Skip TLS verification when LB dials backend control/public (lb mode)")
	lbDynamic        = flag.Bool("lb-dynamic", true, "Accept dynamic barge backend registration (lb mode)")
	lbRegisterTTL    = flag.Int("lb-register-ttl", 45, "Seconds before an unresponsive barge backend is pruned (lb mode)")
	bargeService     = flag.String("barge-service", "server", "Supervised service: 'server' or 'client' (barge mode)")
	bargeReplicas    = flag.Int("barge-replicas", 1, "Number of supervised replicas for horizontal scaling (barge mode)")
	bargePortStep    = flag.Int("barge-port-step", 1, "Port increment between replicas (barge mode)")
	bargeHost        = flag.String("barge-host", "127.0.0.1", "Host advertised for LB backend list (barge mode, server service)")
	bargeRestartDelay = flag.Int("barge-restart-delay", 5, "Seconds to wait before restarting a crashed barge (barge mode)")
	bargeMaxRestarts = flag.Int("barge-max-restarts", 0, "Max restarts per barge (0 = unlimited, barge mode)")
	bargeDashPort    = flag.String("barge-dash", "4050", "Fleet dashboard port (barge mode)")
	bargeBufferScale = flag.Int("barge-buffer-scale", 1, "Vertical scaling multiplier for -buffer (barge mode)")
	bargeStreamScale = flag.Int("barge-stream-scale", 1, "Vertical scaling multiplier for -maxstreams (barge mode)")
	bargeLB          = flag.String("barge-lb", "", "LB public address host:port for automatic backend registration (barge mode, server service)")
	bargeLBHeartbeat = flag.Int("barge-lb-heartbeat", 10, "Seconds between LB registration heartbeats (barge/server mode)")
	bargeFleetID     = flag.String("barge-fleet-id", "", "Fleet identifier sent to LB during registration (barge mode)")
	bargeRuntime     = flag.String("barge-runtime", "k3s", "Barge runtime: 'k3s' (production StatefulSet pods) or 'process' (local supervisor, development)")
	k3sKubeconfig    = flag.String("k3s-kubeconfig", "", "Kubeconfig path for k3s barge runtime (empty: in-cluster, then ~/.kube/config)")
	k3sNamespace     = flag.String("k3s-namespace", "tunneltug", "Kubernetes namespace for barge workloads (k3s runtime)")
	k3sImage         = flag.String("k3s-image", "", "Container image for barge pods (required for k3s; or set TUNNELTUG_K3S_IMAGE)")
	k3sName          = flag.String("k3s-name", "tunneltug-barge", "StatefulSet / app name (k3s runtime)")
	k3sHostNetwork   = flag.Bool("k3s-host-network", true, "Run barge pods with hostNetwork (k3s runtime; needed for QUIC)")
	k3sUpdatePartition = flag.Int("k3s-update-partition", 0, "StatefulSet rollingUpdate.partition (0 = roll all ordinals)")
	k3sCleanup       = flag.Bool("k3s-cleanup", false, "Delete barge StatefulSet on controller shutdown (default: leave pods up)")
	k3sNodeSelector  = flag.String("k3s-node-selector", "", "Comma-separated key=value nodeSelector for barge pods")
	registerLB       = flag.String("register-lb", "", "LB public host:port for server self-registration (server mode / k3s pods)")
	registerHost     = flag.String("register-host", "", "Host address advertised to LB (server self-registration; node IP in k3s)")
	registerFleetID  = flag.String("register-fleet-id", "", "Fleet id for server self-registration (default: hostname)")
	indexFromHostname = flag.Bool("index-from-hostname", false, "Derive replica index from hostname suffix (-N) and apply port bases + step")
	snapshotDir       = flag.String("snapshot-dir", "", "Directory for barge/server state snapshots (empty disables). Restored on start; written on shutdown")
	snapshotOnShutdown = flag.Bool("snapshot-on-shutdown", true, "Write a snapshot before graceful shutdown when -snapshot-dir is set")
	snapshotRestore   = flag.Bool("snapshot-restore", true, "Restore the latest matching snapshot on server start when -snapshot-dir is set")
	snapshotInterval  = flag.Int("snapshot-interval", 0, "Seconds between periodic snapshots (0 = off; only with -snapshot-dir)")
	snapshotKeep      = flag.Int("snapshot-keep", 5, "Number of snapshot files to retain per identity")
	orchDashPort     = flag.String("orch-dash", "4060", "Orchestrator dashboard port (orchestrator mode)")

	// Built-in mesh (secure_dns + secure_registrar). Server/lb become the zone authority;
	// clients publish private names and resolve them via the local VPI stub.
	meshEnabled     = flag.Bool("mesh", false, "Enable built-in mesh network (server/lb: DNS+registrar authority; client: publish + resolve)")
	meshDNS         = flag.String("mesh-dns", "127.0.0.1:5353", "Authoritative mesh DNS listen address (server/lb; UDP+TCP)")
	meshTLD         = flag.String("mesh-tld", "tunnel", "Private TLD operated by this TunnelTug mesh")
	meshZone        = flag.String("mesh-zone", "tunneltug.tunnel", "Product root zone under the private TLD")
	meshNSHost      = flag.String("mesh-ns", "ns.tunneltug.tunnel", "Authoritative NS hostname advertised in zone glue")
	meshEdgeIP      = flag.String("mesh-edge-ip", "", "Public edge IP published for mesh A records (auto-detected when empty)")
	meshDataDir     = flag.String("mesh-data-dir", "", "Mesh state directory (default: ~/.tunneltug/mesh)")
	meshJoinPlatform = flag.Bool("mesh-join-platform", false, "Also join external 0Trust platform mesh (optional; requires gateway/platform)")
	meshPlatform    = flag.String("mesh-platform", "https://0trust.cloud", "External 0Trust platform URL (only with -mesh-join-platform)")
	meshGateway     = flag.String("mesh-gateway", "", "External mesh gateway host:port (optional platform join)")
	meshPubkey      = flag.String("mesh-pubkey", "", "External mesh gateway Noise pubkey hex (optional platform join)")
	meshHost        = flag.String("mesh-host", "", "Mesh host_id for this endpoint (default: -subdomain, or 'direct' in direct routing)")
	meshRegisterURL = flag.String("mesh-register-url", "", "External platform register-mesh URL (default: {platform}/api/v1/access/register-mesh)")
	vpiStub         = flag.Bool("vpi-stub", false, "Run local VPI DNS stub for private TLD resolution (auto-on with -mesh client or -dns)")
	vpiUpstream     = flag.String("vpi-upstream", "", "Authoritative NS for private TLDs (default: server mesh-dns or TRUST_VPI_UPSTREAM_NS)")
	vpiListen       = flag.String("vpi-listen", "127.0.0.1:5354", "VPI stub listen address (UDP)")
	vpiFallback     = flag.String("vpi-fallback", "8.8.8.8:53", "Public DNS fallback for non-private names (host:port or DoH URL)")

	// Product vhost edge (server/lb): co-host apex/www apps next to tunnel subdomains.
	vhostsFile = flag.String("vhosts", "", "Path to product vhost YAML/JSON config (or set TUNNELTUG_VHOSTS)")
	// Private DNS zones: custom TLDs/domains routed to DoH or classic resolvers.
	dnsFileFlag = flag.String("dns", "", "Path to DNS zones YAML/JSON (custom TLDs/domains + DoH; or set TUNNELTUG_DNS)")
)

type certProvider struct {
	controlTLS *tls.Config
	publicTLS  *tls.Config
	acmeMgr    *autocert.Manager
}

type ControlMessage struct {
	Token     string `json:"token"`
	Namespace string `json:"namespace,omitempty"`
	Subdomain string `json:"subdomain"`
}

// liveTunnel is an active control-plane tunnel (yamux over QUIC).
type liveTunnel struct {
	Namespace   string
	Subdomain   string
	Remote      string
	ConnectedAt time.Time
	Session     *yamux.Session
}

type ServerManager struct {
	mu           sync.RWMutex
	tunnels      map[string]*liveTunnel
	pending      map[string]SnapshotTunnel // restored tunnels awaiting client reconnect
	restoredFrom string
	lastSnapshot string
}
