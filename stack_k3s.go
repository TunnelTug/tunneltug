package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0TrustCloud/guikit"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

//go:embed ui/stack_dashboard.gml
var stackDashboardGML string

// Product stack is reconciled entirely inside TunnelTug (client-go).
// No kubectl required — same k3s path as barge fleets.

type stackApp struct {
	Name        string
	Display     string
	Repo        string // hub path under host, e.g. 0trust/williwaw
	Port        int32
	Component   string
	Env         map[string]string
	// YAML overrides (from -stack-config / barge YAML).
	Replicas        int32
	ImageOverride   string // full image ref
	TagOverride     string
	HubHostOverride string
	ConfigFile      string // host path → ConfigMap
	ConfigMount     string // pod path, default /config
	ConfigKey       string // filename in mount
	// Public face (from YAML only — never hardcoded product domains).
	Domain       string // barge hostname
	PublicURL    string // full public base URL
	StackDomain  string // stack-level domain base
	PublicScheme string // http|https
	// Kernel data-replication (ultimate_db / ultimate_keystore).
	NodeID string
	Peers  string // id=url,id2=url
	// Durable host path for /data (empty = emptyDir).
	DataHostPath string
	// WorkingDir is the container working directory (prod: /opt/0trust so ./static works).
	WorkingDir string
	// HostPaths mounts host directories into the pod (prod: /opt/0trust → static, templates, config).
	HostPaths []stackHostPath
	// HostPort: nil = use stack policy; true/false overrides.
	HostPort *bool
	// Args are container args (image ENTRYPOINT stays; these are CMD/args).
	Args []string
}

// stackHostPath is a hostPath volume mount (same model as production dirty cutover).
type stackHostPath struct {
	// Path is the path on the k3s node (often bind-mounted from the controller host).
	Path string
	// Mount is the in-pod mount path.
	Mount string
	// ReadOnly mounts the volume read-only when true.
	ReadOnly bool
}

func stackCatalog() map[string]stackApp {
	return map[string]stackApp{
		"williwaw": {
			Name: "williwaw", Display: "Williwaw", Repo: "0trust/williwaw", Port: 3081, Component: "social-feed",
			Env: map[string]string{
				"WILLIWAW_LISTEN": ":3081",
				"WILLIWAW_DB":     "/data/williwaw.db",
				"WILLIWAW_WAL":    "/data/williwaw.wal",
			},
		},
		"motionkb": {
			Name: "motionkb", Display: "MotionKB", Repo: "0trust/motionkb", Port: 8090, Component: "docs-cms",
			Env: map[string]string{
				"MOTIONKB_LISTEN": ":8090",
				"MOTIONKB_DB":     "/data/motionkb.db",
				"MOTIONKB_WAL":    "/data/motionkb.wal",
			},
		},
		// TodoTraq (Work) — hybrid engineering delivery + CRM; public brand todotraq.com
		"work": {
			Name: "work", Display: "TodoTraq", Repo: "0trust/work", Port: 3098, Component: "todotraq",
			Env: map[string]string{
				"WORK_LISTEN": ":3098",
				"WORK_DB":     "/data/work.db",
				"WORK_WAL":    "/data/work.wal",
			},
		},
		"ack": {
			Name: "ack", Display: "Ack", Repo: "0trust/ack", Port: 3083, Component: "event-chat",
			Env: map[string]string{
				"ACK_LISTEN": ":3083",
				"ACK_DB":     "/data/ack.db",
				"ACK_WAL":    "/data/ack.wal",
			},
		},
		"mail": {
			Name: "mail", Display: "MeshMail", Repo: "0trust/mail", Port: 3086, Component: "mesh-mail",
			Env: map[string]string{
				"MAIL_LISTEN": ":3086",
				"MAIL_DB":     "/data/mail.db",
				"MAIL_WAL":    "/data/mail.wal",
			},
		},
		"search": {
			Name: "search", Display: "MeshSearch", Repo: "0trust/search", Port: 3087, Component: "mesh-search",
			Env: map[string]string{
				"SEARCH_LISTEN": ":3087",
				"SEARCH_DB":     "/data/search.db",
				"SEARCH_WAL":    "/data/search.wal",
			},
		},
		"social": {
			Name: "social", Display: "0Trust CDN", Repo: "0trust/social", Port: 3085, Component: "cdn",
			Env: map[string]string{
				"SOCIAL_LISTEN":   ":3085",
				"SOCIAL_DB":       "/data/social.db",
				"SOCIAL_WAL":      "/data/social.wal",
				"SOCIAL_BLOB_DIR": "/data/blobs",
			},
		},
		"gitmesh": {
			Name: "gitmesh", Display: "GitMesh", Repo: "0trust/gitmesh", Port: 3088, Component: "git-hosting",
			Env: map[string]string{
				"GITMESH_LISTEN":    ":3088",
				"GITMESH_DB":        "/data/gitmesh.db",
				"GITMESH_WAL":       "/data/gitmesh.wal",
				"GITMESH_REPO_ROOT": "/data/repos",
			},
		},
		"drive": {
			Name: "drive", Display: "0Trust Drive", Repo: "0trust/drive", Port: 3095, Component: "drive",
			Env: map[string]string{
				"DRIVE_LISTEN": ":3095",
				"DRIVE_DB":     "/data/drive.db",
				"DRIVE_WAL":    "/data/drive.wal",
			},
		},
		"platform": {
			Name: "platform", Display: "0Trust Platform", Repo: "0trust/platform", Port: 8443, Component: "control-plane",
			Env: map[string]string{
				"TRUST_PORT":     "8443",
				"TRUST_DATA_DIR": "/data",
			},
		},
		"services": {
			Name: "services", Display: "0Trust Services", Repo: "0trust/services", Port: 8080, Component: "mesh-services",
			Env: map[string]string{},
		},
		"name": {
			Name: "name", Display: "0Trust Name (gTLD)", Repo: "0trust/name", Port: 8447, Component: "gtld-face",
			Env: map[string]string{
				"TRUST_PORT":     "8447",
				"TRUST_DATA_DIR": "/data",
				"TRUST_PRODUCT":  "0trust.name",
			},
		},
		"dbsc_relay": {
			Name: "dbsc-relay", Display: "DBSC Relay", Repo: "0trust/dbsc-relay", Port: 8450, Component: "dbsc-relay",
			Env: map[string]string{
				"DBSC_RELAY_LISTEN": ":8450",
			},
		},
		"anycast": {
			Name: "anycast", Display: "Anycast edge", Repo: "0trust/anycast", Port: 9099, Component: "anycast-edge",
			Env: map[string]string{},
		},
		// Platform feature faces — each is its own stack/hub barge (same platform binary).
		"orchid_ingest": platformFace("orchid-ingest", "Orchid Sync Ingest", "0trust/orchid-ingest", 8451, "orchid-ingest", "orchid_ingest"),
		"auth":          platformFace("auth", "0Trust Auth", "0trust/auth", 8460, "idp-auth", "auth"),
		"iam":           platformFace("iam", "0Trust IAM", "0trust/iam", 8461, "iam", "iam"),
		"access":        platformFace("access", "0Trust Access", "0trust/access", 8462, "ztna-access", "access"),
		"scim":          platformFace("scim", "0Trust SCIM", "0trust/scim", 8463, "scim", "scim"),
		"pki":           platformFace("pki", "0Trust PKI", "0trust/pki", 8464, "pki", "pki"),
		"workflows":     platformFace("workflows", "0Trust Workflows", "0trust/workflows", 8465, "workflows", "workflows"),
		"topology":      platformFace("topology", "0Trust Topology", "0trust/topology", 8466, "topology", "topology"),
		"nameservice":   platformFace("nameservice", "0Trust Name Service", "0trust/nameservice", 8467, "nameservice", "nameservice"),
		"servicekeys":   platformFace("servicekeys", "0Trust Service Keys", "0trust/servicekeys", 8468, "service-keys", "servicekeys"),
		"vpi":           platformFace("vpi", "0Trust VPI", "0trust/vpi", 8469, "vpi", "vpi"),
		"logs":          platformFace("logs", "0Trust Logs", "0trust/logs", 8470, "elastic-logs", "logs"),
		// Kernel data-replication barges — peer endpoints so products *replicate*
		// service data (local embeds stay primary; kernel is not a prefer-remote store).
		"ultimate_db": {
			Name: "ultimate-db", Display: "Ultimate DB (kernel replication)", Repo: "0trust/ultimate-db", Port: 8480, Component: "kernel-replication-db",
			Env: map[string]string{
				"UDB_DATA": "/data",
			},
		},
		"ultimate_keystore": {
			Name: "ultimate-keystore", Display: "Ultimate Keystore (kernel replication)", Repo: "0trust/ultimate-keystore", Port: 8481, Component: "kernel-replication-keystore",
			Env: map[string]string{
				"UKS_DATA": "/data",
			},
		},
	}
}

// platformFace builds a stack app for a platform binary feature barge.
func platformFace(deployName, display, repo string, port int32, component, product string) stackApp {
	return stackApp{
		Name: deployName, Display: display, Repo: repo, Port: port, Component: component,
		Env: map[string]string{
			"TRUST_PORT":     fmt.Sprintf("%d", port),
			"TRUST_DATA_DIR": "/data",
			"TRUST_PRODUCT":  product,
		},
	}
}

// productLinkEnv builds PUBLIC_* and inter-service URLs.
// Public faces come from YAML domain/public_url (or stack domain); sibling links use
// each barge's resolved port from the stack (never hardcoded foreign ports/domains).
func productLinkEnv(app stackApp, all []stackApp, ns string) map[string]string {
	pub := resolveBargePublicURL(app, ns)
	out := map[string]string{}
	switch app.Name {
	case "williwaw":
		out["WILLIWAW_PUBLIC_URL"] = pub
		out["WILLIWAW_SOCIAL_CDN_URL"] = stackClusterServiceURL("social", all, ns, 3085)
		out["WILLIWAW_ACK_CHAT_URL"] = stackClusterServiceURL("ack", all, ns, 3083)
	case "motionkb":
		out["MOTIONKB_PUBLIC_URL"] = pub
		out["MOTIONKB_CMS_URL"] = pub
		out["MOTIONKB_SOCIAL_CDN_URL"] = stackClusterServiceURL("social", all, ns, 3085)
		out["MOTIONKB_WILLIWAW_URL"] = stackClusterServiceURL("williwaw", all, ns, 3081)
		out["MOTIONKB_DEFCON_URL"] = stackClusterServiceURL("ack", all, ns, 3083)
	case "work":
		out["WORK_PUBLIC_URL"] = pub
		out["WORK_SOCIAL_CDN"] = stackClusterServiceURL("social", all, ns, 3085)
		out["WORK_WILLIWAW_URL"] = stackClusterServiceURL("williwaw", all, ns, 3081)
		out["WORK_MOTIONKB_URL"] = stackClusterServiceURL("motionkb", all, ns, 8090)
	case "ack":
		out["ACK_PUBLIC_URL"] = pub
		out["ACK_SOCIAL_CDN_URL"] = stackClusterServiceURL("social", all, ns, 3085)
	case "mail":
		out["MAIL_PUBLIC_URL"] = pub
	case "search":
		out["SEARCH_PUBLIC_URL"] = pub
	case "social":
		out["SOCIAL_PUBLIC_URL"] = pub
	case "dbsc-relay":
		out["DBSC_RELAY_PUBLIC"] = pub
	case "platform", "services", "name", "auth", "iam", "access", "scim", "pki",
		"workflows", "topology", "nameservice", "servicekeys", "vpi", "logs", "orchid-ingest":
		out["TRUST_PUBLIC_URL"] = pub
	}
	return out
}

func parseStackProducts(raw string) ([]stackApp, error) {
	raw = strings.TrimSpace(raw)
	cat := stackCatalog()
	var names []string
	if raw == "" || strings.EqualFold(raw, "all") {
		// Default product stack (engine stays on barge fleets).
		names = []string{"social", "ack", "williwaw", "motionkb", "mail", "search", "name", "dbsc_relay", "anycast", "orchid_ingest"}
	} else {
		for _, p := range strings.Split(raw, ",") {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			// Resolve aliases via hub catalog.
			if prod, err := resolveHubProduct(p); err == nil {
				p = prod.Name
			}
			if p == "tunneltug" || p == "engine" || p == "barge" {
				// Engine is for barge fleet STS, not product stack.
				continue
			}
			// Normalize catalog names → stack map keys.
			switch p {
			case "dbsc-relay":
				p = "dbsc_relay"
			case "orchid-ingest", "orchid-sync", "orchid-sync-ingest", "orchid_sync", "orchid_sync_ingest", "orchid":
				p = "orchid_ingest"
			case "idp", "identity":
				p = "auth"
			case "ztna":
				p = "access"
			case "service-keys", "service_keys", "keys":
				p = "servicekeys"
			case "name-service", "name_service":
				p = "nameservice"
			case "elastic", "observability":
				p = "logs"
			case "workflow":
				p = "workflows"
			case "ultimate-db", "udb", "kernel-db":
				p = "ultimate_db"
			case "ultimate-keystore", "uks", "keystore", "kernel-keystore":
				p = "ultimate_keystore"
			case "todotraq", "todo-traq":
				p = "work"
			}
			names = append(names, p)
		}
	}
	var out []stackApp
	seen := map[string]bool{}
	for _, n := range names {
		app, ok := cat[n]
		if !ok {
			// try hub resolve → catalog key
			if prod, err := resolveHubProduct(n); err == nil {
				// map prod.Name to stack key when they differ
				key := prod.Name
				if key == "dbsc_relay" || key == "orchid_ingest" {
					// already stack keys
				}
				app, ok = cat[key]
			}
		}
		if !ok {
			return nil, fmt.Errorf("unknown stack product %q", n)
		}
		if seen[app.Name] {
			continue
		}
		seen[app.Name] = true
		// Catalog ports are zero-config defaults (hub: "Zero Config Port: N").
		syncStackListenEnv(&app)
		out = append(out, app)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no stack products selected")
	}
	return out, nil
}

func stackImage(app stackApp, tag string) string {
	if img := strings.TrimSpace(app.ImageOverride); img != "" {
		return img
	}
	host := strings.TrimSpace(app.HubHostOverride)
	if host == "" {
		host = strings.TrimSpace(*hubHost)
	}
	if host == "" {
		host = "hub.tunneltug.com"
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if t := strings.TrimSpace(app.TagOverride); t != "" {
		tag = t
	}
	if tag == "" {
		tag = strings.TrimSpace(*hubTag)
	}
	if tag == "" {
		tag = "latest"
	}
	return host + "/" + app.Repo + ":" + tag
}

type stackStatus struct {
	mu       sync.RWMutex
	apps     []map[string]any
	appsList []stackApp
	restCfg  *rest.Config
	client   kubernetes.Interface
	ns       string
	tag      string
	hubOn    bool
	err      string
}

func hasStackConfigMount(app stackApp) bool {
	return strings.TrimSpace(app.ConfigFile) != ""
}

func stackMountedConfigPath(app stackApp) string {
	mount := strings.TrimSpace(app.ConfigMount)
	if mount == "" {
		mount = "/config"
	}
	key := strings.TrimSpace(app.ConfigKey)
	if key == "" {
		key = filepath.Base(app.ConfigFile)
		if key == "." || key == "/" || key == "" {
			key = "config.yaml"
		}
	}
	return strings.TrimRight(mount, "/") + "/" + key
}

// k3sCtrAvailable reports whether host-side k3s or ctr can pre-pull images.
func k3sCtrAvailable() bool {
	if _, err := exec.LookPath("k3s"); err == nil {
		return true
	}
	if _, err := exec.LookPath("ctr"); err == nil {
		return true
	}
	return false
}

// publishStackMeshDNS seeds the built-in mesh authority with A records for every
// stack barge (domain / name.zone → mesh edge IP, usually 127.0.0.1 in lab).
func publishStackMeshDNS(auth *meshAuthority, apps []stackApp) {
	if auth == nil {
		return
	}
	edge := resolveMeshEdgeIP()
	if edge == "" {
		edge = "127.0.0.1"
	}
	// Apex of the mesh product zone.
	if z := strings.TrimSpace(*meshZone); z != "" {
		if name, err := auth.PublishA(z, edge); err != nil {
			log.Printf("[mesh] stack apex %s: %v", z, err)
		} else {
			log.Printf("[mesh] stack apex %s → %s", name, edge)
		}
	}
	seen := map[string]bool{}
	publish := func(raw string) {
		raw = strings.ToLower(strings.TrimSpace(raw))
		raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
		if i := strings.IndexByte(raw, '/'); i >= 0 {
			raw = raw[:i]
		}
		if i := strings.IndexByte(raw, ':'); i >= 0 {
			raw = raw[:i]
		}
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		name, err := auth.PublishA(raw, edge)
		if err != nil {
			log.Printf("[mesh] stack publish %s: %v", raw, err)
			return
		}
		log.Printf("[mesh] stack DNS %s → %s", name, edge)
	}
	// stack.{mesh-zone} — auth_proxy + shell operator origin (meshTLS SNI).
	publish("stack")
	for _, app := range apps {
		if d := strings.TrimSpace(app.Domain); d != "" {
			publish(d)
		}
		// Catalog name under mesh zone (e.g. platform.mitigation.local).
		publish(app.Name)
		// Public URL host when set.
		if u := strings.TrimSpace(app.PublicURL); u != "" {
			publish(u)
		}
	}
}

// resolveStackApps loads product barges from -stack-config/-barge-config YAML
// or falls back to -stack-products. Shared by -mode stack and -k3s-stack.
func resolveStackApps() (apps []stackApp, ns, tag string, err error) {
	// Flag defaults to "0trust-stack"; stack YAML namespace wins when present.
	flagNS := strings.TrimSpace(*stackNamespace)
	tag = strings.TrimSpace(*stackTag)
	ns = flagNS

	if cfgPath := stackConfigPath(); cfgPath != "" {
		resolved, loadErr := loadStackConfig(cfgPath)
		if loadErr != nil {
			return nil, "", "", fmt.Errorf("stack-config: %w", loadErr)
		}
		apps = resolved.Apps
		if yamlNS := strings.TrimSpace(resolved.Namespace); yamlNS != "" {
			ns = yamlNS
		}
		if tag == "" {
			tag = resolved.Tag
		}
		if h := strings.TrimSpace(resolved.HubHost); h != "" {
			*hubHost = h
		}
		if hp := strings.TrimSpace(resolved.HostPorts); hp != "" {
			*stackHostPorts = hp
		}
		log.Printf("stack loaded config %s barges=%d namespace=%s tag=%s host_ports=%s",
			cfgPath, len(apps), ns, tag, strings.TrimSpace(*stackHostPorts))
	} else {
		apps, err = parseStackProducts(*stackProducts)
		if err != nil {
			return nil, "", "", err
		}
	}
	if ns == "" {
		ns = "0trust-stack"
	}
	if tag == "" {
		tag = strings.TrimSpace(*hubTag)
	}
	if tag == "" {
		tag = "dev"
	}
	return apps, ns, tag, nil
}

// stackShouldHostPort decides whether a barge binds its zero-config port on the node.
// Policy (-stack-host-ports / YAML host_ports): products | all | none.
// Per-barge host_port: true|false overrides the policy.
func stackShouldHostPort(app stackApp) bool {
	if app.HostPort != nil {
		return *app.HostPort
	}
	mode := strings.ToLower(strings.TrimSpace(*stackHostPorts))
	switch mode {
	case "all", "true", "1", "yes":
		return true
	case "none", "false", "0", "off", "cluster", "clusterip":
		return false
	default: // products
		switch app.Name {
		case "platform", "services":
			return false
		default:
			return true
		}
	}
}

// stackLocalAccessURL is the single-port operator path: always the stack-dash
// gateway /apps/{name}/ so TunnelTug is one entry (vhosts / stack dash) for all
// services. hostPort remains optional for direct node binds.
func stackLocalAccessURL(app stackApp) string {
	dash := strings.TrimSpace(*stackDashPort)
	if dash == "" {
		dash = "4070"
	}
	if *stackGateway {
		return fmt.Sprintf("http://127.0.0.1:%s/apps/%s/", dash, app.Name)
	}
	if stackShouldHostPort(app) {
		return fmt.Sprintf("http://127.0.0.1:%d/", app.Port)
	}
	return fmt.Sprintf("http://127.0.0.1:%s/apps/%s/", dash, app.Name)
}

// stackMeshFQDN is the private mesh DNS name 0TrustOS / gonode provision against
// (stable SNI / WebAuthn-aligned hostname under -mesh-zone).
func stackMeshFQDN(app stackApp) string {
	if d := strings.TrimSpace(app.Domain); d != "" {
		d = strings.ToLower(d)
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		if i := strings.IndexByte(d, '/'); i >= 0 {
			d = d[:i]
		}
		if i := strings.IndexByte(d, ':'); i >= 0 {
			d = d[:i]
		}
		if d != "" {
			return meshPrivateName(d)
		}
	}
	return meshPrivateName(app.Name)
}

// stackMeshURL is the HTTPS mesh address (HTTP/3 SNI host). Port is included
// when the product is not 443 so operators see the full mesh endpoint.
func stackMeshURL(app stackApp) string {
	fqdn := stackMeshFQDN(app)
	if app.Port > 0 && app.Port != 443 && app.Port != 80 {
		return fmt.Sprintf("https://%s:%d/", fqdn, app.Port)
	}
	return fmt.Sprintf("https://%s/", fqdn)
}

func runStack() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	if err := ensureAuthToken(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	token := strings.TrimSpace(*authToken)

	apps, ns, tag, err := resolveStackApps()
	if err != nil {
		log.Fatalf("stack: %v", err)
	}

	// Local mesh DNS (TunnelTug authority + VPI stub) so lab hostnames resolve
	// without editing the public internet DNS — same plane as shield/mesh DoH clients.
	if meshActive() || vpiActive() {
		if meshActive() {
			auth, merr := startMeshAuthority()
			if merr != nil {
				log.Fatalf("stack mesh authority: %v", merr)
			}
			if auth != nil {
				defer auth.Close()
				publishStackMeshDNS(auth, apps)
			}
		}
		startVPIStub(ctx)
	}

	// Embedded hub (same as barge k3s).
	if k3sHubEnabled() {
		if _, err := startK3sBargeHub(ctx, token); err != nil {
			log.Fatalf("stack hub: %v", err)
		}
	}

	// Pre-pull into local k3s via k3s ctr when available. If k3s/ctr is missing
	// (e.g. Windows controller + remote/k3d API), kubelet still pulls from the hub.
	if k3sHubPullEnabled() && !k3sCtrAvailable() {
		log.Printf("k3s/ctr not on PATH — skipping host pre-pull; kubelet will pull hub images")
	} else {
		for _, app := range apps {
			img := stackImage(app, tag)
			if err := ensureK3sBargeImage(ctx, img); err != nil {
				log.Printf("stack pre-pull %s: %v (continuing; kubelet may still pull)", img, err)
			}
		}
	}

	restCfg, err := restConfigFromKubeconfig(strings.TrimSpace(*k3sKubeconfig))
	if err != nil {
		log.Fatalf("stack k3s client: %v", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.Fatalf("stack k3s client: %v", err)
	}

	status := &stackStatus{ns: ns, tag: tag, hubOn: k3sHubEnabled(), appsList: apps, restCfg: restCfg, client: client}
	if err := reconcileProductStack(ctx, client, ns, tag, apps, status); err != nil {
		log.Fatalf("stack reconcile: %v", err)
	}

	if !*quiet {
		go runStackDashboard(ctx, status)
	}
	go watchProductStack(ctx, client, ns, apps, status)

	log.Printf("Product stack online namespace=%s tag=%s apps=%d host_ports=%s gateway=%v (self-contained k3s, no kubectl)",
		ns, tag, len(apps), strings.TrimSpace(*stackHostPorts), *stackGateway)
	log.Printf("Access: dashboard http://127.0.0.1:%s/  gateway http://127.0.0.1:%s/apps/{name}/  auth+shell %s (mesh DNS+TLS only)",
		strings.TrimSpace(*stackDashPort), strings.TrimSpace(*stackDashPort), stackAuthPublicOrigin())
	if meshActive() {
		log.Printf("Mesh DNS: authority %s  VPI stub %s  zone=.%s / %s  edge=%s",
			strings.TrimSpace(*meshDNS), strings.TrimSpace(*vpiListen),
			strings.TrimSpace(*meshTLD), strings.TrimSpace(*meshZone), resolveMeshEdgeIP())
	} else if vpiActive() {
		log.Printf("DNS stub: VPI %s (set -mesh for local authority + lab A records)", strings.TrimSpace(*vpiListen))
	}
	<-ctx.Done()
	if *k3sCleanup {
		delCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, app := range apps {
			_ = client.AppsV1().Deployments(ns).Delete(delCtx, app.Name, metav1.DeleteOptions{})
			_ = client.CoreV1().Services(ns).Delete(delCtx, app.Name, metav1.DeleteOptions{})
			if strings.TrimSpace(app.ConfigFile) != "" {
				_ = client.CoreV1().ConfigMaps(ns).Delete(delCtx, stackConfigMapName(app), metav1.DeleteOptions{})
			}
		}
		log.Printf("stack cleanup: deleted %d apps in %s", len(apps), ns)
	} else {
		log.Println("Product stack controller stopped (workloads left running)")
	}
}

func reconcileProductStack(ctx context.Context, client kubernetes.Interface, ns, tag string, apps []stackApp, status *stackStatus) error {
	if err := ensureK3sNamespace(ctx, client, ns); err != nil {
		return err
	}
	for _, app := range apps {
		img := stackImage(app, tag)
		if err := ensureStackService(ctx, client, ns, app); err != nil {
			return fmt.Errorf("service %s: %w", app.Name, err)
		}
		if err := ensureStackDeployment(ctx, client, ns, apps, app, img); err != nil {
			return fmt.Errorf("deployment %s: %w", app.Name, err)
		}
		log.Printf("stack reconciled %s image=%s zero_config_or_yaml_port=%d", app.Name, img, app.Port)
	}
	_ = refreshStackStatus(ctx, client, ns, apps, tag, status)
	return nil
}

func stackLabels(app stackApp) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       app.Name,
		"app.kubernetes.io/instance":   app.Name,
		"app.kubernetes.io/component":  app.Component,
		"app.kubernetes.io/part-of":    "0trust-stack",
		"app.kubernetes.io/managed-by": "tunneltug",
	}
}

func ensureStackService(ctx context.Context, client kubernetes.Interface, ns string, app stackApp) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: ns,
			Labels:    stackLabels(app),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/name": app.Name},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       app.Port,
				TargetPort: intstr.FromInt32(app.Port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	existing, err := client.CoreV1().Services(ns).Get(ctx, app.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Spec.Selector = svc.Spec.Selector
	existing.Spec.Ports = svc.Spec.Ports
	existing.Labels = svc.Labels
	_, err = client.CoreV1().Services(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func stackConfigMapName(app stackApp) string {
	return app.Name + "-config"
}

// adaptStackConfigContent rewrites shared platform YAML so each face listens on its
// zero-config port (TRUST_PORT / hostPort). Without this, every face inherits
// server.port: 8443 from a lab config and readiness on :8460/:8461/… fails.
func adaptStackConfigContent(raw []byte, app stackApp) []byte {
	if app.Port <= 0 || len(raw) == 0 {
		return raw
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil || doc == nil {
		return raw
	}
	server, _ := doc["server"].(map[string]interface{})
	if server == nil {
		// Not a platform-style config; leave unchanged.
		return raw
	}
	port := strconv.Itoa(int(app.Port))
	server["port"] = port
	// Lab configs often dual-bind quic on the same port as HTTP.
	if _, ok := server["quic_port"]; ok {
		server["quic_port"] = port
	}
	if host := strings.TrimSpace(app.Domain); host != "" {
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		if host != "" {
			server["host"] = host
		}
	}
	if pub := strings.TrimSpace(app.PublicURL); pub != "" {
		server["domain"] = pub
	} else if host, _ := server["host"].(string); strings.TrimSpace(host) != "" {
		scheme := strings.TrimSpace(app.PublicScheme)
		if scheme == "" {
			scheme = "http"
		}
		server["domain"] = fmt.Sprintf("%s://%s:%s", scheme, host, port)
	}
	doc["server"] = server
	out, err := yaml.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// ensureStackConfigMap loads app.ConfigFile from the controller host into a ConfigMap.
func ensureStackConfigMap(ctx context.Context, client kubernetes.Interface, ns string, app stackApp) error {
	if strings.TrimSpace(app.ConfigFile) == "" {
		return nil
	}
	raw, err := os.ReadFile(app.ConfigFile)
	if err != nil {
		return fmt.Errorf("read config_file %s: %w", app.ConfigFile, err)
	}
	raw = adaptStackConfigContent(raw, app)
	key := strings.TrimSpace(app.ConfigKey)
	if key == "" {
		key = filepath.Base(app.ConfigFile)
		if key == "." || key == "/" || key == "" {
			key = "config.yaml"
		}
	}
	name := stackConfigMapName(app)
	labels := stackLabels(app)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
			Annotations: map[string]string{
				"tunneltug.io/managed":     "stack",
				"tunneltug.io/config-file": app.ConfigFile,
				"tunneltug.io/listen-port": strconv.Itoa(int(app.Port)),
			},
		},
		Data: map[string]string{key: string(raw)},
	}
	existing, err := client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = cm.Data
	existing.Labels = cm.Labels
	existing.Annotations = cm.Annotations
	_, err = client.CoreV1().ConfigMaps(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func ensureStackDeployment(ctx context.Context, client kubernetes.Interface, ns string, all []stackApp, app stackApp, image string) error {
	if err := ensureStackConfigMap(ctx, client, ns, app); err != nil {
		return err
	}
	labels := stackLabels(app)
	// Honor 0 (scale-to-zero when host systemd owns the public face / hostPort).
	replicas := app.Replicas
	if replicas < 0 {
		replicas = 1
	}
	// Build env: generated links + kernel replication URLs first, then catalog/YAML env
	// so SRE env: overrides always win (including public URLs).
	merged := map[string]string{}
	for k, v := range productLinkEnv(app, all, ns) {
		merged[k] = v
	}
	// Kernel replication peers: inject ultimate_db / keystore service DNS so product
	// barges can AddPeer for data replication (local embeds remain; no prefer-remote).
	if app.Name != "ultimate-db" && app.Name != "ultimate-keystore" {
		for k, v := range stackKernelEnv(ns, all...) {
			merged[k] = v
		}
	}
	for k, v := range app.Env {
		merged[k] = v
	}
	// Advertise mounted config path for apps that look for TUNNELTUG_CONFIG / TRUST_CONFIG.
	if cf := strings.TrimSpace(app.ConfigFile); cf != "" {
		mount := strings.TrimSpace(app.ConfigMount)
		if mount == "" {
			mount = "/config"
		}
		key := strings.TrimSpace(app.ConfigKey)
		if key == "" {
			key = filepath.Base(cf)
		}
		cfgPath := strings.TrimRight(mount, "/") + "/" + key
		if _, ok := merged["TUNNELTUG_CONFIG"]; !ok {
			merged["TUNNELTUG_CONFIG"] = cfgPath
		}
		if _, ok := merged["TRUST_CONFIG"]; !ok {
			merged["TRUST_CONFIG"] = cfgPath
		}
	}
	// 0TrustOS product barges: each container is a gonode sidecar + product unit.
	// entrypoint 0trustos-entry provisions the node when OTRUST_TUNNEL_TOKEN is set.
	use0TrustOS := *stack0TrustOS && stackAppUses0TrustOS(app)
	if use0TrustOS {
		if _, ok := merged["OTRUST_PRODUCT"]; !ok {
			merged["OTRUST_PRODUCT"] = app.Name
		}
		if _, ok := merged["TRUST_PRODUCT"]; !ok {
			merged["TRUST_PRODUCT"] = app.Name
		}
		portStr := strconv.FormatInt(int64(app.Port), 10)
		if _, ok := merged["OTRUST_PRODUCT_PORT"]; !ok {
			merged["OTRUST_PRODUCT_PORT"] = portStr
		}
		if _, ok := merged["TRUST_PORT"]; !ok {
			merged["TRUST_PORT"] = portStr
		}
		if _, ok := merged["PRODUCT_BIN"]; !ok {
			// Outside hostPath /opt/0trust so node static mounts cannot hide the binary.
			merged["PRODUCT_BIN"] = "/usr/local/bin/product"
		}
		if _, ok := merged["GONODE_DATA"]; !ok {
			merged["GONODE_DATA"] = "/var/lib/gonode"
		}
		// Product CLI args → entry env (image ENTRYPOINT is 0trustos-entry, not product).
		if len(app.Args) > 0 {
			if _, ok := merged["PRODUCT_ARGS"]; !ok {
				merged["PRODUCT_ARGS"] = strings.Join(app.Args, " ")
			}
		} else if app.Name == "services" && hasStackConfigMount(app) {
			// services needs --config; 0TrustOS unit exec gets PRODUCT_ARGS.
			if _, ok := merged["PRODUCT_ARGS"]; !ok {
				merged["PRODUCT_ARGS"] = "--config " + stackMountedConfigPath(app) + " --mode server"
			}
		}
		if tok := strings.TrimSpace(*authToken); tok != "" {
			if _, ok := merged["OTRUST_TUNNEL_TOKEN"]; !ok {
				merged["OTRUST_TUNNEL_TOKEN"] = tok
			}
		}
		// Mesh domain/PKI face: gonode phones home to 0trust.cloud, pulls meshTLS,
		// and publishes this name to nameservice (ns.0trust.cloud / ns.0trust.name).
		meshFQDN := stackMeshFQDN(app)
		if _, ok := merged["OTRUST_MESH_DOMAIN"]; !ok {
			merged["OTRUST_MESH_DOMAIN"] = meshFQDN
		}
		if _, ok := merged["MESH_HOST"]; !ok {
			merged["MESH_HOST"] = app.Name
		}
		if _, ok := merged["OTRUST_MESH_URL"]; !ok {
			merged["OTRUST_MESH_URL"] = stackMeshURL(app)
		}
		if _, ok := merged["TRUST_PUBLIC_URL"]; !ok {
			merged["TRUST_PUBLIC_URL"] = stackMeshURL(app)
		}
		if _, ok := merged["OTRUST_PLATFORM_URL"]; !ok {
			merged["OTRUST_PLATFORM_URL"] = "https://0trust.cloud"
		}
		if _, ok := merged["OTRUST_MESH_EDGE"]; !ok {
			if edge := strings.TrimSpace(*meshEdgeIP); edge != "" {
				merged["OTRUST_MESH_EDGE"] = edge
			}
		}
		// Sync token for nameservice provision + meshtls cert pull (same as tunnel token in lab).
		if tok := strings.TrimSpace(*authToken); tok != "" {
			if _, ok := merged["OTRUST_SYNC_TOKEN"]; !ok {
				merged["OTRUST_SYNC_TOKEN"] = tok
			}
		}
	}
	env := make([]corev1.EnvVar, 0, len(merged))
	for k, v := range merged {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	// Bind product listen ports on the node so host edge / operators reach barges
	// without kubectl (http://127.0.0.1:{port}). Policy: -stack-host-ports / host_ports /
	// per-barge host_port. platform/services default off so bare-metal can own :8443/:443.
	httpPort := corev1.ContainerPort{
		Name: "http", ContainerPort: app.Port, Protocol: corev1.ProtocolTCP,
	}
	if stackShouldHostPort(app) {
		httpPort.HostPort = app.Port
	}
	// Local k3d / offline: IfNotPresent so rebuilt hub.* tags import via ctr without a pull.
	// Online hub dogfood still picks up newer digests on next deploy when tags move.
	pullPolicy := corev1.PullIfNotPresent
	if k3sHubEnabled() && k3sHubPullEnabled() {
		pullPolicy = corev1.PullAlways
	}
	c := corev1.Container{
		Name:            app.Name,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Ports:           []corev1.ContainerPort{httpPort},
		Env:             env,
		VolumeMounts: []corev1.VolumeMount{{
			Name: "data", MountPath: "/data",
		}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(app.Port)},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       5,
			TimeoutSeconds:      2,
			FailureThreshold:    12,
		},
	}
	if use0TrustOS {
		// Durable gonode identity / provision.json beside product data.
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name: "gonode", MountPath: "/var/lib/gonode",
		})
		// 0TrustOS entry owns CMD — product argv is PRODUCT_ARGS (see above).
		// Only special modes (anycast / ultimate_*) set c.Args below.
	}
	// Prod systemd uses WorkingDirectory=/opt/0trust so relative static/ and templates/ resolve.
	if wd := strings.TrimSpace(app.WorkingDir); wd != "" {
		c.WorkingDir = wd
	}
	// Anycast image is the TunnelTug binary; run the anycast edge mode.
	if app.Name == "anycast" {
		anycastCfg := "/config/anycast.yaml"
		if key := strings.TrimSpace(app.ConfigKey); key != "" {
			mount := strings.TrimSpace(app.ConfigMount)
			if mount == "" {
				mount = "/config"
			}
			anycastCfg = strings.TrimRight(mount, "/") + "/" + key
		}
		c.Args = []string{"-mode", "anycast", "-anycast-config", anycastCfg}
	}
	// Kernel data-replication barges — TunnelTug binary; peers/node_id from YAML.
	if app.Name == "ultimate-db" {
		nodeID := strings.TrimSpace(app.NodeID)
		if nodeID == "" {
			nodeID = "ultimate-db"
		}
		c.Args = []string{
			"-mode", "ultimate_db",
			"-udb-listen", fmt.Sprintf(":%d", app.Port),
			"-udb-data", "/data",
			"-udb-node-id", nodeID,
		}
		if peers := strings.TrimSpace(app.Peers); peers != "" {
			c.Args = append(c.Args, "-udb-peers", peers)
		}
	}
	if app.Name == "ultimate-keystore" {
		nodeID := strings.TrimSpace(app.NodeID)
		if nodeID == "" {
			nodeID = "ultimate-keystore"
		}
		c.Args = []string{
			"-mode", "ultimate_keystore",
			"-uks-listen", fmt.Sprintf(":%d", app.Port),
			"-uks-data", "/data",
			"-uks-node-id", nodeID,
		}
	}
	// Explicit YAML args win / append after mode-specific defaults when set.
	// For 0TrustOS product faces, args are PRODUCT_ARGS env (entry launches product unit).
	if len(app.Args) > 0 && !use0TrustOS {
		c.Args = append([]string(nil), app.Args...)
	}
	// services binary does not read TRUST_CONFIG; inject --config when a barge config is mounted.
	if app.Name == "services" && hasStackConfigMount(app) && len(app.Args) == 0 && !use0TrustOS {
		cfgPath := stackMountedConfigPath(app)
		c.Args = []string{"--config", cfgPath, "--mode", "server"}
	}
	// Platform-binary faces: TRUST_PORT already set via app.Env (name:8447, orchid-ingest:8451).
	dataVol := corev1.Volume{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	if hp := strings.TrimSpace(app.DataHostPath); hp != "" {
		t := corev1.HostPathDirectoryOrCreate
		dataVol.VolumeSource = corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: hp, Type: &t},
		}
	}
	vols := []corev1.Volume{dataVol}
	if use0TrustOS {
		vols = append(vols, corev1.Volume{
			Name: "gonode",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	// Prod-style host tree (e.g. /opt/0trust with static/, templates/, config/).
	for i, hp := range app.HostPaths {
		host := strings.TrimSpace(hp.Path)
		mnt := strings.TrimSpace(hp.Mount)
		if host == "" || mnt == "" {
			continue
		}
		volName := fmt.Sprintf("hostpath-%d", i)
		t := corev1.HostPathDirectoryOrCreate
		vols = append(vols, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: host, Type: &t},
			},
		})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name: volName, MountPath: mnt, ReadOnly: hp.ReadOnly,
		})
	}

	// Mount per-barge YAML config when configured (SRE: load barge with yaml config).
	hasYAMLConfig := hasStackConfigMount(app)
	if hasYAMLConfig {
		mount := strings.TrimSpace(app.ConfigMount)
		if mount == "" {
			mount = "/config"
		}
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name: "barge-config", MountPath: mount, ReadOnly: true,
		})
		vols = append(vols, corev1.Volume{
			Name: "barge-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: stackConfigMapName(app)},
				},
			},
		})
	} else if app.Name == "anycast" {
		// Legacy optional ConfigMap when no stack YAML config_file is set.
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "anycast-config", MountPath: "/config"})
		vols = append(vols, corev1.Volume{
			Name: "anycast-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "anycast-config"},
					Optional:             boolPtr(true),
				},
			},
		})
	}
	// hostPort is exclusive per node. RollingUpdate maxSurge leaves a second
	// pod Pending forever ("didn't have free ports"). Recreate tears down first.
	strategy := appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
	if stackShouldHostPort(app) {
		strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: ns,
			Labels:    labels,
			Annotations: map[string]string{
				"tunneltug.io/managed": "stack",
				"tunneltug.io/image":   image,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: strategy,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": app.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{c},
					Volumes:    vols,
				},
			},
		},
	}
	existing, err := client.AppsV1().Deployments(ns).Get(ctx, app.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Spec = dep.Spec
	existing.Labels = dep.Labels
	existing.Annotations = dep.Annotations
	_, err = client.AppsV1().Deployments(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func boolPtr(b bool) *bool { return &b }

// stackAppUses0TrustOS reports whether the barge should run as a 0TrustOS product
// container (gonode sidecar entry + product unit). Kernel / pure TunnelTug modes
// keep their own -mode args on the tunneltug binary image.
func stackAppUses0TrustOS(app stackApp) bool {
	switch app.Name {
	case "ultimate-db", "ultimate_db", "ultimate-keystore", "ultimate_keystore", "anycast":
		return false
	default:
		return true
	}
}

func watchProductStack(ctx context.Context, client kubernetes.Interface, ns string, apps []stackApp, status *stackStatus) {
	tag := status.tag
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = refreshStackStatus(ctx, client, ns, apps, tag, status)
		}
	}
}

func refreshStackStatus(ctx context.Context, client kubernetes.Interface, ns string, apps []stackApp, tag string, status *stackStatus) error {
	rows := make([]map[string]any, 0, len(apps))
	for _, app := range apps {
		desired := app.Replicas
		if desired < 0 {
			desired = 1
		}
		row := map[string]any{
			"name":        app.Name,
			"display":     app.Display,
			"image":       stackImage(app, tag),
			"port":        app.Port,
			"ready":       0,
			"desired":     desired,
			"phase":       "Unknown",
			"host_port":   stackShouldHostPort(app),
			"access_url":  stackLocalAccessURL(app), // one-port gateway when -stack-gateway
			"gateway":     fmt.Sprintf("/apps/%s/", app.Name),
			"mesh_fqdn":   stackMeshFQDN(app),
			"mesh_url":    stackMeshURL(app),
			"mesh_host":   app.Name,
			"0trustos":    stackAppUses0TrustOS(app) && *stack0TrustOS,
		}
		dep, err := client.AppsV1().Deployments(ns).Get(ctx, app.Name, metav1.GetOptions{})
		if err == nil {
			row["ready"] = dep.Status.ReadyReplicas
			// Prefer spec.replicas (desired) over status.replicas (includes terminating RSes).
			if dep.Spec.Replicas != nil {
				row["desired"] = *dep.Spec.Replicas
			} else {
				row["desired"] = dep.Status.Replicas
			}
			if dep.Status.ReadyReplicas > 0 {
				row["phase"] = "Ready"
			} else if dep.Status.UnavailableReplicas > 0 {
				row["phase"] = "Progressing"
			} else {
				row["phase"] = "Pending"
			}
		} else {
			row["phase"] = "Missing"
			row["error"] = err.Error()
		}
		rows = append(rows, row)
	}
	status.mu.Lock()
	status.apps = rows
	status.err = ""
	status.mu.Unlock()
	return nil
}

func runStackDashboard(ctx context.Context, status *stackStatus) {
	mux := http.NewServeMux()
	mux.HandleFunc("/_tunneltug/stack", func(w http.ResponseWriter, r *http.Request) {
		status.mu.RLock()
		defer status.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		meshZone := strings.TrimSpace(*meshZone)
		if meshZone == "" {
			meshZone = "tunneltug.tunnel"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "ok",
			"mode":       "stack",
			"namespace":  status.ns,
			"tag":        status.tag,
			"hub":        status.hubOn,
			"host_ports": strings.TrimSpace(*stackHostPorts),
			"gateway":    *stackGateway,
			"mesh_zone":  meshZone,
			"mesh_dns":   strings.TrimSpace(*meshDNS),
			"vpi":        strings.TrimSpace(*vpiListen),
			"auth":       stackAuthStatusJSON(),
			"apps":       status.apps,
			"access": map[string]string{
				// Loopback: status + app gateway only (no WebAuthn).
				"dashboard": fmt.Sprintf("http://127.0.0.1:%s/", strings.TrimSpace(*stackDashPort)),
				"json":      fmt.Sprintf("http://127.0.0.1:%s/_tunneltug/stack", strings.TrimSpace(*stackDashPort)),
				"apps":      fmt.Sprintf("http://127.0.0.1:%s/apps/{name}/", strings.TrimSpace(*stackDashPort)),
				// Auth + shell: mesh DNS + meshTLS HTTPS only (WebAuthn cannot use http/localhost).
				"auth":         stackAuthPublicOrigin() + "/auth",
				"shell":        stackAuthPublicOrigin() + "/shell?app={name}",
				"shell_ws":     strings.Replace(stackAuthPublicOrigin(), "https://", "wss://", 1) + "/shell/ws?app={name}",
				"mesh_host":    stackOperatorMeshHost(),
				"shell_target": "0trustos-k8s-image",
			},
			"note": "auth+shell only on mesh HTTPS (mesh DNS + meshTLS); WebAuthn forbids http/localhost; gonode Junos CLI after passkey+DBSC",
		})
	})
	// 0Trust auth_proxy on meshTLS face: /auth WebAuthn + DBSC (not loopback HTTP).
	mountStackAuthProxy(mux)
	// Built-in gateway: reverse-proxy to in-cluster Services via the k8s API (no kubectl).
	if *stackGateway {
		mux.Handle("/apps/", stackAppsGateway(status))
	}
	// Webshell: mesh HTTPS + auth_proxy → gonode Junos CLI in 0TrustOS image.
	mountStackWebshell(mux, status)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// GML dashboard (guikit) — same markup language as product UIs.
		guikit.ServeGML(w, stackDashboardGML, stackDashboardData(status))
	})
	addr := "127.0.0.1:" + strings.TrimSpace(*stackDashPort)
	if _, err := strconv.Atoi(strings.TrimSpace(*stackDashPort)); err != nil {
		addr = "127.0.0.1:4070"
	}
	// Host-based mesh on the same port: http://platform.mitigation.mesh:4070/ after hosts/NRPT.
	// meshTLS HTTPS on -mesh-tls-port for WebAuthn/DBSC (SNI = mesh FQDN).
	handler := http.Handler(mux)
	if *stackGateway {
		handler = stackMeshHostGateway(status, mux)
	}
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()
	log.Printf("Product stack dashboard http://%s/  JSON http://%s/_tunneltug/stack  gateway http://%s/apps/{name}/", addr, addr, addr)
	log.Printf("Auth+shell (mesh DNS + meshTLS HTTPS only): %s/auth  %s/shell?app={name}", stackAuthPublicOrigin(), stackAuthPublicOrigin())
	if meshActive() {
		zone := strings.TrimSpace(*meshZone)
		log.Printf("Mesh one-port: after hosts/NRPT open http://{app}.%s:%s/  (authority %s VPI %s)",
			zone, strings.TrimSpace(*stackDashPort), strings.TrimSpace(*meshDNS), strings.TrimSpace(*vpiListen))
	}
	// HTTPS meshTLS face: product Host gateway + auth_proxy WebAuthn + gonode shell.
	// Mesh DNS must resolve stack.{zone} / {app}.{zone}; browsers need mesh-root trust.
	if meshTLSActive() {
		if err := ensureMeshTLS(); err != nil {
			log.Printf("[meshtls] stack: %v", err)
		} else {
			// Ensure leaf for operator face (stack.{zone}) used by WebAuthn RP ID / SNI.
			if _, err := ensureMeshTLSLeaf(stackOperatorMeshHost()); err != nil {
				log.Printf("[meshtls] stack operator leaf: %v", err)
			}
			tlsPort := strings.TrimSpace(*meshTLSPort)
			if tlsPort == "" {
				tlsPort = "4073"
			}
			tlsAddr := "0.0.0.0:" + tlsPort
			meshHandler := stackMeshTLSHandler(status, mux)
			tlsSrv := &http.Server{
				Addr:              tlsAddr,
				Handler:           meshHandler,
				TLSConfig:         meshTLSServerConfig(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			go func() {
				<-ctx.Done()
				shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = tlsSrv.Shutdown(shCtx)
			}()
			go func() {
				zone := strings.TrimSpace(*meshZone)
				log.Printf("MeshTLS HTTPS https://{app}.%s:%s/  auth+shell %s  (mesh-root %s)",
					zone, tlsPort, stackAuthPublicOrigin(), meshTLSDir())
				if err := tlsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
					log.Printf("meshTLS gateway: %v", err)
				}
			}()
		}
	} else if stackAuthEnabled() {
		log.Printf("[stack-auth] WARNING: -mesh-tls off — WebAuthn/shell require meshTLS + mesh DNS; enable -mesh-tls")
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("stack dashboard: %v", err)
	}
}

// stackDashboardData builds the template map for ui/stack_dashboard.gml.
func stackDashboardData(status *stackStatus) map[string]interface{} {
	status.mu.RLock()
	defer status.mu.RUnlock()
	rows := make([]map[string]interface{}, 0, len(status.apps))
	for _, a := range status.apps {
		phase := fmt.Sprint(a["phase"])
		phaseClass := "phase-prog"
		switch phase {
		case "Ready":
			phaseClass = "phase-ready"
		case "Missing", "Unknown":
			phaseClass = "phase-bad"
		}
		gw := fmt.Sprint(a["gateway"])
		name := fmt.Sprint(a["name"])
		meshFQDN := fmt.Sprint(a["mesh_fqdn"])
		if meshFQDN == "" || meshFQDN == "<nil>" {
			meshFQDN = meshPrivateName(name)
		}
		meshURL := fmt.Sprint(a["mesh_url"])
		if meshURL == "" || meshURL == "<nil>" {
			meshURL = "https://" + meshFQDN + "/"
		}
		// Operator-facing mesh open: meshTLS HTTPS when available (WebAuthn/DBSC FQDN),
		// else plain Host routing on stack-dash.
		dash := strings.TrimSpace(*stackDashPort)
		if dash == "" {
			dash = "4070"
		}
		meshOpen := fmt.Sprintf("http://%s:%s/", meshFQDN, dash)
		if meshTLSActive() {
			tlsPort := strings.TrimSpace(*meshTLSPort)
			if tlsPort == "" {
				tlsPort = "4073"
			}
			meshOpen = fmt.Sprintf("https://%s:%s/", meshFQDN, tlsPort)
		}
		// Prefer gateway as primary open when one-port model is on.
		openURL := fmt.Sprint(a["access_url"])
		if *stackGateway && gw != "" && gw != "<nil>" {
			openURL = gw
		}
		rows = append(rows, map[string]interface{}{
			"Name":        name,
			"Image":       fmt.Sprint(a["image"]),
			"Phase":       phase,
			"PhaseClass":  phaseClass,
			"Ready":       a["ready"],
			"Desired":     a["desired"],
			"Port":        a["port"],
			"AccessURL":   openURL,
			"GatewayURL":  gw,
			"ShowGateway": *stackGateway && gw != "" && gw != "<nil>",
			// Shell only on mesh HTTPS (auth_proxy + WebAuthn RP ID).
			"ShellURL":    stackAuthPublicOrigin() + "/shell?app=" + name,
			"MeshFQDN":    meshFQDN,
			"MeshURL":     meshOpen,
			"MeshTLSURL":  meshURL,
			"ZeroTrustOS": a["0trustos"] == true,
		})
	}
	gwOn := "off"
	if *stackGateway {
		gwOn = "on"
	}
	meshZone := strings.TrimSpace(*meshZone)
	if meshZone == "" {
		meshZone = "tunneltug.tunnel"
	}
	return map[string]interface{}{
		"Title":        "TunnelTug product stack",
		"Namespace":    status.ns,
		"Tag":          status.tag,
		"HostPorts":    strings.TrimSpace(*stackHostPorts),
		"GatewayOn":    gwOn,
		"MeshZone":     meshZone,
		"MeshAuthURL":  stackAuthPublicOrigin() + "/auth",
		"MeshShellURL": stackAuthPublicOrigin() + "/shell",
		"Apps":         rows,
		"Note": "Auth+shell: " + stackAuthPublicOrigin() + " (Mesh CA FQDN TLS via gonode path; not http/localhost). Gateway: /apps/{name}/ on loopback.",
	}
}

// stackLookupApp finds a catalog app by name (or underscore/dash variants).
func stackLookupApp(status *stackStatus, name string) *stackApp {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	status.mu.RLock()
	defer status.mu.RUnlock()
	for i := range status.appsList {
		a := &status.appsList[i]
		if a.Name == name || strings.ReplaceAll(a.Name, "_", "-") == name {
			return a
		}
	}
	return nil
}

// stackLookupAppByMeshHost maps Host platform.mitigation.mesh → stackApp.
func stackLookupAppByMeshHost(status *stackStatus, host string) *stackApp {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return nil
	}
	status.mu.RLock()
	defer status.mu.RUnlock()
	for i := range status.appsList {
		a := &status.appsList[i]
		if stackMeshFQDN(*a) == host {
			return a
		}
		// Bare product name under zone (name.zone).
		if meshPrivateName(a.Name) == host {
			return a
		}
	}
	return nil
}

// rewriteStackGatewayLocation rewrites backend absolute-path redirects so the
// browser stays under /apps/{name}/ (fixes 302 → /welcome → dash 404).
// pathPrefix empty means Host-based mesh (no prefix; leave absolute paths alone).
func rewriteStackGatewayLocation(pathPrefix, backendHost, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" || pathPrefix == "" {
		return loc
	}
	prefix := strings.TrimRight(pathPrefix, "/")
	if strings.HasPrefix(loc, prefix+"/") || loc == prefix {
		return loc
	}
	// Absolute-path redirect from the product (e.g. Location: /welcome).
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		return prefix + loc
	}
	u, err := url.Parse(loc)
	if err != nil || u.Host == "" {
		return loc
	}
	// Same-host or backend hostPort redirects → keep path under gateway prefix.
	h := strings.ToLower(u.Hostname())
	bh := strings.ToLower(backendHost)
	if bh != "" {
		if bhh, _, err := net.SplitHostPort(bh); err == nil {
			bh = bhh
		}
	}
	if h == "127.0.0.1" || h == "localhost" || h == bh || strings.HasSuffix(h, ".svc") || strings.Contains(h, ".svc.") {
		path := u.Path
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			path = prefix + path
		}
		q := ""
		if u.RawQuery != "" {
			q = "?" + u.RawQuery
		}
		return path + q
	}
	return loc
}

func rewriteStackGatewayResponse(pathPrefix, backendHost string, resp *http.Response) {
	if pathPrefix == "" {
		return
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if next := rewriteStackGatewayLocation(pathPrefix, backendHost, loc); next != loc {
			resp.Header.Set("Location", next)
		}
	}
	// Cookies scoped to / must be scoped under the gateway prefix.
	prefix := strings.TrimRight(pathPrefix, "/")
	if prefix == "" {
		return
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}
	resp.Header.Del("Set-Cookie")
	for _, c := range cookies {
		if !strings.Contains(strings.ToLower(c), "path=") {
			resp.Header.Add("Set-Cookie", c+"; Path="+prefix+"/")
			continue
		}
		// Path=/ → Path=/apps/name/
		lower := strings.ToLower(c)
		if i := strings.Index(lower, "path=/"); i >= 0 {
			rest := c[i+len("path=/"):]
			// only bare root path
			if rest == "" || rest[0] == ';' || rest[0] == ' ' {
				end := i + len("path=/")
				for end < len(c) && c[end] != ';' {
					end++
				}
				pathVal := c[i+len("Path=") : end]
				if pathVal == "/" {
					c = c[:i] + "Path=" + prefix + "/" + c[end:]
				}
			}
		}
		resp.Header.Add("Set-Cookie", c)
	}
}

// proxyStackApp reverse-proxies one stack product. pathPrefix is /apps/{name} for
// path gateway, or "" for Host-based mesh names on the same dash port.
func proxyStackApp(status *stackStatus, app *stackApp, pathPrefix, subPath string, w http.ResponseWriter, r *http.Request) {
	if app == nil {
		http.Error(w, "unknown stack app", http.StatusNotFound)
		return
	}
	if subPath == "" {
		subPath = "/"
	}
	if !strings.HasPrefix(subPath, "/") {
		subPath = "/" + subPath
	}
	backendHost := fmt.Sprintf("127.0.0.1:%d", app.Port)

	// Prefer direct hostPort when bound (fast path on the node).
	if stackShouldHostPort(*app) {
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = "http"
				req.URL.Host = backendHost
				req.URL.Path = subPath
				req.URL.RawPath = ""
				req.Host = backendHost
				// Preserve original mesh Host when Host-based so apps see SNI/WebAuthn name.
				if pathPrefix == "" && r.Host != "" {
					req.Host = r.Host
					req.Header.Set("X-Forwarded-Host", r.Host)
				} else if pathPrefix != "" {
					req.Header.Set("X-Forwarded-Prefix", strings.TrimRight(pathPrefix, "/"))
					req.Header.Set("X-Forwarded-Host", r.Host)
				}
				req.Header.Set("X-Forwarded-Proto", "http")
			},
			ModifyResponse: func(resp *http.Response) error {
				rewriteStackGatewayResponse(pathPrefix, backendHost, resp)
				return nil
			},
			ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
				http.Error(rw, "gateway: "+err.Error(), http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(w, r)
		return
	}

	status.mu.RLock()
	cfg := status.restCfg
	ns := status.ns
	status.mu.RUnlock()
	if cfg == nil {
		http.Error(w, "stack gateway: no kube rest config", http.StatusServiceUnavailable)
		return
	}
	if err := proxyViaKubeService(cfg, ns, app.Name, app.Port, subPath, pathPrefix, backendHost, r, w); err != nil {
		http.Error(w, "gateway: "+err.Error(), http.StatusBadGateway)
	}
}

// stackAppsGateway proxies /apps/{name}/… to the product (hostPort or k8s service proxy).
func stackAppsGateway(status *stackStatus) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /apps/{name}/optional/path
		restPath := strings.TrimPrefix(r.URL.Path, "/apps/")
		if restPath == "" || restPath == r.URL.Path {
			http.Error(w, "usage: /apps/{name}/", http.StatusBadRequest)
			return
		}
		name, sub, _ := strings.Cut(restPath, "/")
		name = strings.TrimSpace(name)
		if name == "" {
			http.Error(w, "missing app name", http.StatusBadRequest)
			return
		}
		app := stackLookupApp(status, name)
		if app == nil {
			http.Error(w, "unknown stack app "+name, http.StatusNotFound)
			return
		}
		prefix := "/apps/" + app.Name
		subPath := "/" + sub
		if sub == "" {
			// /apps/name with no trailing slash → /apps/name/
			if !strings.HasSuffix(r.URL.Path, "/") && r.URL.Path == prefix {
				http.Redirect(w, r, prefix+"/", http.StatusFound)
				return
			}
			subPath = "/"
		}
		proxyStackApp(status, app, prefix, subPath, w, r)
	})
}

// stackMeshHostGateway serves products by Host: {app}.zone on the stack-dash port
// (true one-port: mesh FQDN → 127.0.0.1:4070 after hosts/NRPT).
func stackMeshHostGateway(status *stackStatus, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app := stackLookupAppByMeshHost(status, r.Host); app != nil {
			// Loopback dash control paths (JSON + 0TrustOS shell) stay on reserved prefixes.
			if strings.HasPrefix(r.URL.Path, "/_tunneltug/") || strings.HasPrefix(r.URL.Path, "/shell") {
				next.ServeHTTP(w, r)
				return
			}
			proxyStackApp(status, app, "", r.URL.Path, w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// stackMeshTLSHandler is the LAN meshTLS + mesh DNS face:
// product Host gateway, auth_proxy (/auth WebAuthn+DBSC), and gonode shell.
// WebAuthn cannot use http:// or localhost — this HTTPS mesh FQDN face is required.
func stackMeshTLSHandler(status *stackStatus, mux http.Handler) http.Handler {
	base := mux
	if *stackGateway {
		base = stackMeshHostGateway(status, mux)
	}
	return base
}

func proxyViaKubeService(cfg *rest.Config, ns, name string, port int32, path, pathPrefix, backendHost string, r *http.Request, w http.ResponseWriter) error {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return err
	}
	host := strings.TrimRight(cfg.Host, "/")
	// cfg.Host may be "https://127.0.0.1:6443" already.
	// Service proxy form: http:{service}:{port} (colons must not be path-escaped).
	svcName := fmt.Sprintf("http:%s:%d", name, port)
	apiPath := fmt.Sprintf("/api/v1/namespaces/%s/services/%s/proxy%s", ns, svcName, path)
	u, err := url.Parse(host + apiPath)
	if err != nil {
		return err
	}
	if r.URL.RawQuery != "" {
		u.RawQuery = r.URL.RawQuery
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), r.Body)
	if err != nil {
		return err
	}
	// Copy select headers; kube auth is applied by TransportFor(cfg).
	for k, vv := range r.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "authorization" {
			continue
		}
		for _, v := range vv {
			out.Header.Add(k, v)
		}
	}
	client := &http.Client{Transport: transport, Timeout: 120 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(out)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rewriteStackGatewayResponse(pathPrefix, backendHost, resp)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return nil
}
