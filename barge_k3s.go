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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type k3sBargeConfig struct {
	Namespace       string
	Name            string
	Image           string
	Replicas        int32
	HostNetwork     bool
	UpdatePartition int32
	NodeSelector    map[string]string
	ControlBase     string
	PublicBase      string
	PortStep        int
	Token           string
	Domain          string
	LBAddr          string
	FleetID         string
	NamespaceLogic  string
	BackendInsecure bool
	HTTP3           bool
	SnapshotDir     string
}

type k3sPodSnapshot struct {
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Ready    bool   `json:"ready"`
	Node     string `json:"node,omitempty"`
	HostIP   string `json:"host_ip,omitempty"`
	PodIP    string `json:"pod_ip,omitempty"`
	Control  string `json:"control,omitempty"`
	Public   string `json:"public,omitempty"`
	Restarts int32  `json:"restarts"`
	Ordinal  int    `json:"ordinal"`
}

type k3sFleetStatus struct {
	mu       sync.RWMutex
	pods     []k3sPodSnapshot
	replicas int
	ready    int
	image    string
	name     string
	ns       string
	err      string
	hubOn    bool
	hubURL   string
	sdf      *bargeSDF
}

func runBargeK3s() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	cfg, err := loadK3sBargeConfig()
	if err != nil {
		log.Fatalf("Barge k3s configuration error: %v", err)
	}

	// One-shot: publish a local k3s image to the barge hub, then continue (or exit if only-publish).
	if src := strings.TrimSpace(*k3sHubPublish); src != "" {
		dest := cfg.Image
		if err := publishK3sBargeImage(ctx, src, dest, cfg.Token); err != nil {
			log.Fatalf("k3s barge hub publish: %v", err)
		}
		if *k3sHubPublishOnly {
			log.Printf("Published %s → %s; exiting (-k3s-hub-publish-only)", src, dest)
			return
		}
	}

	// Image hub is part of this k3s fleet controller (public pull / auth push).
	hub, err := startK3sBargeHub(ctx, cfg.Token)
	if err != nil {
		log.Fatalf("k3s hub: %v", err)
	}
	if hub != nil {
		log.Printf("k3s image hub online public=%s engine=%s", hub.cfg.Public, cfg.Image)
	}

	// SDF-attested fleet manifests (secure_data_format).
	sdf, err := newBargeSDF("")
	if err != nil {
		log.Printf("SDF fleet manifests unavailable: %v (reconcile continues unsigned)", err)
		sdf = nil
	} else {
		defer sdf.Close()
	}

	// Ensure engine image is in local k3s/containerd before reconciling pods.
	if err := ensureK3sBargeImage(ctx, cfg.Image); err != nil {
		log.Printf("k3s engine image ensure: %v (continuing; kubelet may pull)", err)
	}

	client, err := newKubernetesClient(strings.TrimSpace(*k3sKubeconfig))
	if err != nil {
		log.Fatalf("Barge k3s client error: %v", err)
	}

	status := &k3sFleetStatus{
		replicas: int(cfg.Replicas),
		image:    cfg.Image,
		name:     cfg.Name,
		ns:       cfg.Namespace,
		hubOn:    k3sHubEnabled(),
		hubURL:   strings.TrimRight(strings.TrimSpace(*hubPublic), "/"),
		sdf:      sdf,
	}

	log.Printf("Starting k3s fleet (barge mode): %d server replica(s) in %s/%s engine %s",
		cfg.Replicas, cfg.Namespace, cfg.Name, cfg.Image)
	if cfg.LBAddr != "" {
		log.Printf("Pods will self-register with LB %s", cfg.LBAddr)
	}
	if k3sHubEnabled() {
		log.Printf("Image hub embedded in k3s fleet controller listen %s", strings.TrimSpace(*hubListen))
	}

	if err := reconcileK3sBargeSDF(ctx, client, cfg, sdf); err != nil {
		log.Fatalf("Barge k3s reconcile error: %v", err)
	}

	// Optional: product apps (williwaw, motionkb, …) via same controller — still no kubectl.
	// Load from -stack-config / -barge-config YAML when set (SRE: each barge is configurable).
	if *k3sStack {
		apps, stackNS, tag, err := resolveStackApps()
		if err != nil {
			log.Printf("k3s-stack products: %v", err)
		} else {
			for _, app := range apps {
				if err := ensureK3sBargeImage(ctx, stackImage(app, tag)); err != nil {
					log.Printf("k3s-stack pull %s: %v", app.Name, err)
				}
			}
			st := &stackStatus{ns: stackNS, tag: tag, hubOn: k3sHubEnabled()}
			if err := reconcileProductStack(ctx, client, stackNS, tag, apps, st); err != nil {
				log.Printf("k3s-stack reconcile: %v", err)
			} else {
				log.Printf("k3s-stack online namespace=%s apps=%d (self-contained, yaml-configurable)", stackNS, len(apps))
				if !*quiet {
					go runStackDashboard(ctx, st)
				}
				go watchProductStack(ctx, client, stackNS, apps, st)
			}
		}
	}

	if !*quiet {
		go runK3sBargeDashboard(ctx, status)
	}

	go watchK3sBargePods(ctx, client, cfg, status)

	<-ctx.Done()
	if *k3sCleanup {
		log.Printf("k3s cleanup: deleting StatefulSet %s/%s", cfg.Namespace, cfg.Name)
		delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		policy := metav1.DeletePropagationForeground
		_ = client.AppsV1().StatefulSets(cfg.Namespace).Delete(delCtx, cfg.Name, metav1.DeleteOptions{
			PropagationPolicy: &policy,
		})
	} else {
		log.Println("Barge k3s controller stopped (workloads left running; hub stopped with controller)")
	}
}

func loadK3sBargeConfig() (k3sBargeConfig, error) {
	img := strings.TrimSpace(*k3sImage)
	if img == "" {
		img = defaultK3sBargeImage
		*k3sImage = img
	}
	cfg := k3sBargeConfig{
		Namespace:       strings.TrimSpace(*k3sNamespace),
		Name:            strings.TrimSpace(*k3sName),
		Image:           img,
		Replicas:        int32(*bargeReplicas),
		HostNetwork:     *k3sHostNetwork,
		UpdatePartition: int32(*k3sUpdatePartition),
		ControlBase:     strings.TrimSpace(*controlPort),
		PublicBase:      strings.TrimSpace(*publicPort),
		PortStep:        *bargePortStep,
		Token:           strings.TrimSpace(*authToken),
		Domain:          strings.TrimSpace(*domain),
		LBAddr:          strings.TrimSpace(*bargeLB),
		FleetID:         strings.TrimSpace(*bargeFleetID),
		NamespaceLogic:  normalizeNamespace(*namespace),
		BackendInsecure: *backendInsecure || *insecure,
		HTTP3:           *http3Enabled,
		SnapshotDir:     strings.TrimSpace(*snapshotDir),
	}
	if cfg.SnapshotDir == "" {
		// Persist across pod restarts on the same node (hostNetwork fleets).
		cfg.SnapshotDir = "/var/lib/tunneltug/snapshots"
	}
	if cfg.LBAddr == "" {
		cfg.LBAddr = strings.TrimSpace(*registerLB)
	}
	if cfg.FleetID == "" {
		cfg.FleetID = cfg.Name
	}
	sel, err := parseNodeSelector(strings.TrimSpace(*k3sNodeSelector))
	if err != nil {
		return cfg, err
	}
	cfg.NodeSelector = sel
	return cfg, nil
}

func parseNodeSelector(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	out := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" {
			return nil, fmt.Errorf("invalid -k3s-node-selector entry %q (want key=value)", part)
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out, nil
}

func newKubernetesClient(kubeconfig string) (*kubernetes.Clientset, error) {
	config, err := restConfigFromKubeconfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func restConfigFromKubeconfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(path); err == nil {
		return clientcmd.BuildConfigFromFlags("", path)
	}
	return nil, fmt.Errorf("no kubeconfig: set -k3s-kubeconfig, run in-cluster, or provide ~/.kube/config")
}

func reconcileK3sBarge(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig) error {
	return reconcileK3sBargeSDF(ctx, client, cfg, nil)
}

func reconcileK3sBargeSDF(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig, sdf *bargeSDF) error {
	if err := ensureK3sNamespace(ctx, client, cfg.Namespace); err != nil {
		return err
	}

	// Resolve image digest from local k3s, then SDF-sign fleet shape + image identity.
	deployImage := cfg.Image
	var sdfToken, sdfRoot, imageDigest string
	if sdf != nil {
		att, err := resolveImageAttestation(ctx, cfg.Image)
		if err != nil {
			log.Printf("SDF image digest: %v (signing fleet shape only if possible)", err)
			// Still try sign — SignFleetReconcileWithImage requires digest; fall back without.
		} else {
			imageDigest = att.Digest
			// Prefer digest-pinned image for the StatefulSet so kubelet cannot drift on tag.
			if att.Pinned != "" {
				deployImage = att.Pinned
			}
			tok, root, serr := sdf.SignFleetReconcileWithImage(ctx, cfg, att)
			if serr != nil {
				log.Printf("SDF manifest (fleet+image): %v", serr)
			} else {
				sdfToken, sdfRoot = tok, root
				// Continuous integrity check
				if ok, detail, verr := sdf.VerifyImage(ctx); verr != nil || !ok {
					log.Printf("SDF image verify FAILED: %v detail=%v", verr, detail)
				} else if !*quiet {
					log.Printf("SDF image verify OK digest=%s", truncateMid(imageDigest, 19))
				}
			}
		}
	}

	cfgDeploy := cfg
	cfgDeploy.Image = deployImage

	if err := ensureK3sConfigMapSDF(ctx, client, cfgDeploy, sdfToken, sdfRoot, imageDigest); err != nil {
		return err
	}
	if err := ensureK3sHeadlessService(ctx, client, cfg); err != nil {
		return err
	}
	return ensureK3sStatefulSetSDF(ctx, client, cfgDeploy, sdfToken, sdfRoot, imageDigest)
}

func ensureK3sHeadlessService(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    bargeK3sLabels(cfg),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector: map[string]string{
				"app.kubernetes.io/name":     cfg.Name,
				"app.kubernetes.io/instance": cfg.Name,
			},
			Ports: []corev1.ServicePort{
				{Name: "control", Port: mustAtoi(cfg.ControlBase), Protocol: corev1.ProtocolUDP},
				{Name: "public", Port: mustAtoi(cfg.PublicBase), Protocol: corev1.ProtocolTCP},
			},
		},
	}
	_, err := client.CoreV1().Services(cfg.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().Services(cfg.Namespace).Create(ctx, svc, metav1.CreateOptions{})
		return err
	}
	return err
}

func ensureK3sNamespace(ctx context.Context, client kubernetes.Interface, name string) error {
	_, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	return err
}

func ensureK3sConfigMap(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig) error {
	return ensureK3sConfigMapSDF(ctx, client, cfg, "", "", "")
}

func ensureK3sConfigMapSDF(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig, sdfToken, sdfRoot, imageDigest string) error {
	cm := buildBargeConfigMapSDF(cfg, sdfToken, sdfRoot, imageDigest)
	existing, err := client.CoreV1().ConfigMaps(cfg.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().ConfigMaps(cfg.Namespace).Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = cm.Data
	existing.Labels = cm.Labels
	existing.Annotations = cm.Annotations
	_, err = client.CoreV1().ConfigMaps(cfg.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func ensureK3sStatefulSet(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig) error {
	return ensureK3sStatefulSetSDF(ctx, client, cfg, "", "", "")
}

func ensureK3sStatefulSetSDF(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig, sdfToken, sdfRoot, imageDigest string) error {
	desired := buildBargeStatefulSet(cfg)
	if desired.Annotations == nil {
		desired.Annotations = map[string]string{}
	}
	desired.Annotations["tunneltug.io/format"] = "secure_data_format"
	desired.Annotations["tunneltug.io/integrity"] = "image+fleet"
	if sdfRoot != "" {
		desired.Annotations["tunneltug.io/sdf-state-root"] = sdfRoot
	}
	if imageDigest != "" {
		desired.Annotations["tunneltug.io/image-digest"] = imageDigest
	}
	if sdfToken != "" {
		if jti := extractJWTClaim(sdfToken, "jti"); jti != "" {
			desired.Annotations["tunneltug.io/sdf-jti"] = jti
		}
	}
	existing, err := client.AppsV1().StatefulSets(cfg.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.AppsV1().StatefulSets(cfg.Namespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}

	// Before rolling an image/template change, ask live pods to snapshot tunnel inventory.
	if bargeTemplateChanged(existing, desired) {
		requestK3sPodSnapshots(ctx, client, cfg)
	}

	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template = desired.Spec.Template
	existing.Spec.UpdateStrategy = desired.Spec.UpdateStrategy
	existing.Labels = desired.Labels
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	for k, v := range desired.Annotations {
		existing.Annotations[k] = v
	}
	_, err = client.AppsV1().StatefulSets(cfg.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func bargeTemplateChanged(existing, desired *appsv1.StatefulSet) bool {
	if existing == nil || desired == nil {
		return false
	}
	oldImg, newImg := "", ""
	if len(existing.Spec.Template.Spec.Containers) > 0 {
		oldImg = existing.Spec.Template.Spec.Containers[0].Image
	}
	if len(desired.Spec.Template.Spec.Containers) > 0 {
		newImg = desired.Spec.Template.Spec.Containers[0].Image
	}
	if oldImg != newImg {
		return true
	}
	// Replica-only changes do not need a pre-update snapshot of every pod.
	return false
}

func requestK3sPodSnapshots(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig) {
	list, err := client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s", cfg.Name, cfg.Name),
	})
	if err != nil {
		log.Printf("pre-update snapshot: list pods: %v", err)
		return
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	for _, p := range list.Items {
		if !podReady(p) {
			continue
		}
		ord, err := hostnameOrdinal(p.Name)
		if err != nil {
			continue
		}
		public, err := portForIndex(cfg.PublicBase, ord, cfg.PortStep)
		if err != nil {
			continue
		}
		host := p.Status.HostIP
		if host == "" {
			host = p.Status.PodIP
		}
		if host == "" {
			continue
		}
		url := fmt.Sprintf("http://%s:%s/_tunneltug/snapshot?token=%s", host, public, cfg.Token)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("X-TunnelTug-Token", cfg.Token)
		resp, err := httpClient.Do(req)
		if err != nil {
			log.Printf("pre-update snapshot %s: %v", p.Name, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Printf("pre-update snapshot ok for pod %s", p.Name)
		} else {
			log.Printf("pre-update snapshot %s: HTTP %d", p.Name, resp.StatusCode)
		}
	}
}

func buildBargeConfigMap(cfg k3sBargeConfig) *corev1.ConfigMap {
	return buildBargeConfigMapSDF(cfg, "", "", "")
}

func buildBargeConfigMapSDF(cfg k3sBargeConfig, sdfToken, sdfRoot, imageDigest string) *corev1.ConfigMap {
	data := map[string]string{
		"token":           cfg.Token,
		"domain":          cfg.Domain,
		"lb":              cfg.LBAddr,
		"fleet_id":        cfg.FleetID,
		"control_base":    cfg.ControlBase,
		"public_base":     cfg.PublicBase,
		"port_step":       strconv.Itoa(cfg.PortStep),
		"logic_namespace": cfg.NamespaceLogic,
		"image":           cfg.Image,
		"replicas":        strconv.Itoa(int(cfg.Replicas)),
		"runtime":         "k3s",
		// Integrity: digest proves image bytes; SDF token binds fleet+digest.
		"integrity": "image_digest+fleet_shape",
	}
	anns := map[string]string{
		"tunneltug.io/runtime":   "k3s",
		"tunneltug.io/format":    "secure_data_format",
		"tunneltug.io/integrity": "image+fleet",
	}
	if imageDigest != "" {
		data["image_digest"] = imageDigest
		anns["tunneltug.io/image-digest"] = imageDigest
	}
	if sdfToken != "" {
		data["sdf_manifest"] = sdfToken
		data["sdf_state_root"] = sdfRoot
		anns["tunneltug.io/sdf-state-root"] = sdfRoot
		if jti := extractJWTClaim(sdfToken, "jti"); jti != "" {
			anns["tunneltug.io/sdf-jti"] = jti
		}
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cfg.Name + "-config",
			Namespace:   cfg.Namespace,
			Labels:      bargeK3sLabels(cfg),
			Annotations: anns,
		},
		Data: data,
	}
}

func bargeK3sLabels(cfg k3sBargeConfig) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       cfg.Name,
		"app.kubernetes.io/instance":   cfg.Name,
		"app.kubernetes.io/component":  "barge",
		"app.kubernetes.io/managed-by": "tunneltug",
	}
}

func buildBargeStatefulSet(cfg k3sBargeConfig) *appsv1.StatefulSet {
	labels := bargeK3sLabels(cfg)
	replicas := cfg.Replicas
	partition := cfg.UpdatePartition
	args := buildK3sServerArgs(cfg)

	env := []corev1.EnvVar{
		{
			Name: "TUNNELTUG_REGISTER_HOST",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"},
			},
		},
	}
	if cfg.LBAddr != "" {
		env = append(env, corev1.EnvVar{Name: "TUNNELTUG_REGISTER_LB", Value: cfg.LBAddr})
	}
	if cfg.Token != "" {
		env = append(env, corev1.EnvVar{Name: "TUNNELTUG_TOKEN", Value: cfg.Token})
	}
	if cfg.Domain != "" {
		env = append(env, corev1.EnvVar{Name: "TUNNELTUG_DOMAIN", Value: cfg.Domain})
	}
	env = append(env, corev1.EnvVar{Name: "TUNNELTUG_INDEX_FROM_HOSTNAME", Value: "true"})
	if cfg.SnapshotDir != "" {
		env = append(env, corev1.EnvVar{Name: "TUNNELTUG_SNAPSHOT_DIR", Value: cfg.SnapshotDir})
	}

	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyAlways,
		Containers: []corev1.Container{
			{
				Name:            "tunneltug",
				Image: cfg.Image,
				// Hub images: controller pre-pulls via k3s ctr; IfNotPresent avoids thrash.
				// Always re-pull when image is the default hub tag so rolls pick up latest.
				ImagePullPolicy: k3sImagePullPolicy(cfg.Image),
				Args:            args,
				Env:             env,
				Ports: []corev1.ContainerPort{
					{Name: "control", ContainerPort: mustAtoi(cfg.ControlBase), Protocol: corev1.ProtocolUDP},
					{Name: "public", ContainerPort: mustAtoi(cfg.PublicBase), Protocol: corev1.ProtocolTCP},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "snapshots", MountPath: cfg.SnapshotDir},
				},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "snapshots",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: cfg.SnapshotDir,
						Type: hostPathDirOrCreate(),
					},
				},
			},
		},
	}
	if cfg.HostNetwork {
		podSpec.HostNetwork = true
		podSpec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
	}
	if len(cfg.NodeSelector) > 0 {
		podSpec.NodeSelector = cfg.NodeSelector
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: cfg.Name,
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name":     cfg.Name,
					"app.kubernetes.io/instance": cfg.Name,
				},
			},
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: &partition,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

func hostPathDirOrCreate() *corev1.HostPathType {
	t := corev1.HostPathDirectoryOrCreate
	return &t
}

func k3sImagePullPolicy(image string) corev1.PullPolicy {
	// Floating hub tags should re-check the registry; digests / local tags stay pinned.
	if strings.Contains(image, "hub.tunneltug.com/") && (strings.HasSuffix(image, ":latest") || strings.Contains(image, "/tunneltug/engine") || strings.Contains(image, "/tunneltug/barge")) && !strings.Contains(image, "@sha256:") {
		if strings.HasSuffix(image, ":latest") || strings.Contains(image, "/tunneltug/") {
			return corev1.PullAlways
		}
	}
	if strings.Contains(image, "hub.tunneltug.com/") && strings.HasSuffix(image, ":latest") {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}

func buildK3sServerArgs(cfg k3sBargeConfig) []string {
	args := []string{
		"-mode", "server",
		"-token", cfg.Token,
		"-routing", strings.TrimSpace(*routing),
		"-namespace", cfg.NamespaceLogic,
		"-control", cfg.ControlBase,
		"-public", cfg.PublicBase,
		"-barge-port-step", strconv.Itoa(cfg.PortStep),
		"-index-from-hostname",
		"-keepalive", strconv.Itoa(*keepAlive),
		"-buffer", strconv.Itoa(*streamBuffer),
		"-maxstreams", strconv.Itoa(*maxStreams),
		"-quiet",
	}
	if cfg.SnapshotDir != "" {
		args = append(args,
			"-snapshot-dir", cfg.SnapshotDir,
			"-snapshot-on-shutdown=true",
			"-snapshot-restore=true",
		)
	}
	if cfg.LBAddr != "" {
		args = append(args, "-register-lb", cfg.LBAddr)
		// register-host comes from env TUNNELTUG_REGISTER_HOST (Downward API)
	}
	if cfg.FleetID != "" {
		// Per-pod fleet id defaults to hostname; optional prefix via register-fleet-id empty
		// Leave empty so each pod uses its hostname as fleet id for uniqueness.
	}
	if cfg.BackendInsecure {
		args = append(args, "-backend-insecure")
	}
	if !cfg.HTTP3 {
		args = append(args, "-http3=false")
	}
	if *prod {
		args = append(args, "-prod")
	}
	if *dev {
		args = append(args, "-dev")
	}
	if cfg.Domain != "" {
		args = append(args, "-domain", cfg.Domain)
	}
	if email := strings.TrimSpace(*email); email != "" {
		args = append(args, "-email", email)
	}
	if subalt := strings.TrimSpace(*subalt); subalt != "" {
		args = append(args, "-subalt", subalt)
	}
	return args
}

func mustAtoi(s string) int32 {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return int32(n)
}

func watchK3sBargePods(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig, status *k3sFleetStatus) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	refresh := func() {
		if err := refreshK3sPodStatus(ctx, client, cfg, status); err != nil && !*quiet {
			status.mu.Lock()
			status.err = err.Error()
			status.mu.Unlock()
		}
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func refreshK3sPodStatus(ctx context.Context, client kubernetes.Interface, cfg k3sBargeConfig, status *k3sFleetStatus) error {
	list, err := client.CoreV1().Pods(cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/instance=%s", cfg.Name, cfg.Name),
	})
	if err != nil {
		return err
	}

	pods := make([]k3sPodSnapshot, 0, len(list.Items))
	ready := 0
	for _, p := range list.Items {
		ord, _ := hostnameOrdinal(p.Name)
		control, _ := portForIndex(cfg.ControlBase, ord, cfg.PortStep)
		public, _ := portForIndex(cfg.PublicBase, ord, cfg.PortStep)
		hostIP := p.Status.HostIP
		snap := k3sPodSnapshot{
			Name:    p.Name,
			Phase:   string(p.Status.Phase),
			Ready:   podReady(p),
			Node:    p.Spec.NodeName,
			HostIP:  hostIP,
			PodIP:   p.Status.PodIP,
			Control: hostIP + ":" + control,
			Public:  hostIP + ":" + public,
			Ordinal: ord,
		}
		for _, cs := range p.Status.ContainerStatuses {
			snap.Restarts += cs.RestartCount
		}
		if snap.Ready {
			ready++
		}
		pods = append(pods, snap)
	}

	status.mu.Lock()
	status.pods = pods
	status.ready = ready
	status.replicas = int(cfg.Replicas)
	status.err = ""
	status.mu.Unlock()
	return nil
}

func podReady(p corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	// Without readiness probes, treat Running as ready once containers are ready.
	if len(p.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

func runK3sBargeDashboard(ctx context.Context, status *k3sFleetStatus) {
	mux := http.NewServeMux()

	mux.HandleFunc("/_tunneltug/barges", func(w http.ResponseWriter, r *http.Request) {
		status.mu.RLock()
		defer status.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{
			"status":    "ok",
			"mode":      "barge",
			"runtime":   "k3s",
			"service":   "server",
			"replicas":  status.replicas,
			"running":   status.ready,
			"image":     status.image,
			"name":      status.name,
			"namespace": status.ns,
			"barges":    status.pods,
			"hub": map[string]any{
				"enabled": status.hubOn,
				"public":  status.hubURL,
				"listen":  strings.TrimSpace(*hubListen),
				"pull":    "public",
				"push":    "authenticated",
				"layer":   "k3s-fleet",
			},
			"sdf_manifest": func() map[string]any {
				if status.sdf != nil {
					// Re-verify image on each status poll so tampering is visible.
					_, _, _ = status.sdf.VerifyImage(r.Context())
					return status.sdf.Status()
				}
				return map[string]any{"enabled": false}
			}(),
		}
		if status.err != "" {
			payload["error"] = status.err
		}
		if lb := strings.TrimSpace(*bargeLB); lb != "" {
			payload["lb_registration"] = lb
		}
		_ = json.NewEncoder(w).Encode(payload)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		status.mu.RLock()
		pods := append([]k3sPodSnapshot(nil), status.pods...)
		ready := status.ready
		replicas := status.replicas
		ns := status.ns
		name := status.name
		errMsg := status.err
		img := status.image
		hubOn := status.hubOn
		hubURL := status.hubURL
		status.mu.RUnlock()

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h2>TunnelTug Barge Fleet (k3s)</h2>`)
		fmt.Fprintf(w, `<p>StatefulSet: %s/%s | Ready: %d/%d | Image: <code>%s</code> | <a href="/_tunneltug/barges">JSON</a></p>`, ns, name, ready, replicas, img)
		if hubOn {
			fmt.Fprintf(w, `<p>Image hub (built into k3s layer): <code>%s</code> listen <code>%s</code> — public pull, auth push</p>`, hubURL, strings.TrimSpace(*hubListen))
		}
		if errMsg != "" {
			fmt.Fprintf(w, `<p style="color:red">Error: %s</p>`, errMsg)
		}
		fmt.Fprint(w, `<table border="1" cellpadding="6"><tr><th>Pod</th><th>Phase</th><th>Ready</th><th>Node</th><th>Control</th><th>Public</th><th>Restarts</th></tr>`)
		for _, p := range pods {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%v</td><td>%s</td><td>%s</td><td>%s</td><td>%d</td></tr>`,
				p.Name, p.Phase, p.Ready, p.Node, p.Control, p.Public, p.Restarts)
		}
		fmt.Fprint(w, `</table></body></html>`)
	})

	addr := "127.0.0.1:" + *bargeDashPort
	log.Printf("Barge k3s dashboard at http://%s", addr)
	dash := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = dash.Shutdown(shutdownCtx)
	}()
	if err := dash.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("Barge k3s dashboard stopped: %v", err)
	}
}

