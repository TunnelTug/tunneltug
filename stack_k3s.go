package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

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
	ns       string
	tag      string
	hubOn    bool
	err      string
}

// resolveStackApps loads product barges from -stack-config/-barge-config YAML
// or falls back to -stack-products. Shared by -mode stack and -k3s-stack.
func resolveStackApps() (apps []stackApp, ns, tag string, err error) {
	ns = strings.TrimSpace(*stackNamespace)
	tag = strings.TrimSpace(*stackTag)

	if cfgPath := stackConfigPath(); cfgPath != "" {
		resolved, loadErr := loadStackConfig(cfgPath)
		if loadErr != nil {
			return nil, "", "", fmt.Errorf("stack-config: %w", loadErr)
		}
		apps = resolved.Apps
		if ns == "" {
			ns = resolved.Namespace
		}
		if tag == "" {
			tag = resolved.Tag
		}
		if h := strings.TrimSpace(resolved.HubHost); h != "" {
			*hubHost = h
		}
		log.Printf("stack loaded config %s barges=%d namespace=%s tag=%s", cfgPath, len(apps), ns, tag)
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

	// Embedded hub (same as barge k3s).
	if k3sHubEnabled() {
		if _, err := startK3sBargeHub(ctx, token); err != nil {
			log.Fatalf("stack hub: %v", err)
		}
	}

	// Pull each product image into local k3s via k3s ctr (no kubectl).
	// Images without a registry manifest cannot run — fail that barge hard.
	for _, app := range apps {
		img := stackImage(app, tag)
		if err := ensureK3sBargeImage(ctx, img); err != nil {
			log.Fatalf("stack pull %s: %v (registry manifest required to run)", img, err)
		}
	}

	client, err := newKubernetesClient(strings.TrimSpace(*k3sKubeconfig))
	if err != nil {
		log.Fatalf("stack k3s client: %v", err)
	}

	status := &stackStatus{ns: ns, tag: tag, hubOn: k3sHubEnabled()}
	if err := reconcileProductStack(ctx, client, ns, tag, apps, status); err != nil {
		log.Fatalf("stack reconcile: %v", err)
	}

	if !*quiet {
		go runStackDashboard(ctx, status)
	}
	go watchProductStack(ctx, client, ns, apps, status)

	log.Printf("Product stack online namespace=%s tag=%s apps=%d (self-contained k3s, no kubectl)", ns, tag, len(apps))
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

// ensureStackConfigMap loads app.ConfigFile from the controller host into a ConfigMap.
func ensureStackConfigMap(ctx context.Context, client kubernetes.Interface, ns string, app stackApp) error {
	if strings.TrimSpace(app.ConfigFile) == "" {
		return nil
	}
	raw, err := os.ReadFile(app.ConfigFile)
	if err != nil {
		return fmt.Errorf("read config_file %s: %w", app.ConfigFile, err)
	}
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
	replicas := int32(1)
	if app.Replicas > 0 {
		replicas = app.Replicas
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
	env := make([]corev1.EnvVar, 0, len(merged))
	for k, v := range merged {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	c := corev1.Container{
		Name:            app.Name,
		Image:           image,
		ImagePullPolicy: corev1.PullAlways,
		Ports: []corev1.ContainerPort{{
			Name: "http", ContainerPort: app.Port, Protocol: corev1.ProtocolTCP,
		}},
		Env: env,
		VolumeMounts: []corev1.VolumeMount{{
			Name: "data", MountPath: "/data",
		}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(app.Port)},
			},
			InitialDelaySeconds: 3,
			PeriodSeconds:       5,
		},
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
	// Platform-binary faces: TRUST_PORT already set via app.Env (name:8447, orchid-ingest:8451).
	vols := []corev1.Volume{{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}}

	// Mount per-barge YAML config when configured (SRE: load barge with yaml config).
	hasYAMLConfig := strings.TrimSpace(app.ConfigFile) != ""
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
		desired := int32(1)
		if app.Replicas > 0 {
			desired = app.Replicas
		}
		row := map[string]any{
			"name":    app.Name,
			"display": app.Display,
			"image":   stackImage(app, tag),
			"port":    app.Port,
			"ready":   0,
			"desired": desired,
			"phase":   "Unknown",
		}
		dep, err := client.AppsV1().Deployments(ns).Get(ctx, app.Name, metav1.GetOptions{})
		if err == nil {
			row["ready"] = dep.Status.ReadyReplicas
			row["desired"] = dep.Status.Replicas
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    "ok",
			"mode":      "stack",
			"namespace": status.ns,
			"tag":       status.tag,
			"hub":       status.hubOn,
			"apps":      status.apps,
			"note":      "self-contained k3s reconcile — no kubectl",
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		status.mu.RLock()
		apps := append([]map[string]any(nil), status.apps...)
		ns := status.ns
		tag := status.tag
		status.mu.RUnlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body><h2>TunnelTug product stack</h2>`)
		fmt.Fprintf(w, `<p>namespace <code>%s</code> tag <code>%s</code> · self-contained (no kubectl) · <a href="/_tunneltug/stack">JSON</a></p><ul>`, ns, tag)
		for _, a := range apps {
			fmt.Fprintf(w, `<li><b>%v</b> %v — %v ready · <code>%v</code></li>`,
				a["name"], a["phase"], a["ready"], a["image"])
		}
		fmt.Fprint(w, `</ul></body></html>`)
	})
	addr := "127.0.0.1:" + strings.TrimSpace(*stackDashPort)
	if _, err := strconv.Atoi(strings.TrimSpace(*stackDashPort)); err != nil {
		addr = "127.0.0.1:4070"
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()
	log.Printf("Product stack dashboard http://%s/_tunneltug/stack", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("stack dashboard: %v", err)
	}
}
