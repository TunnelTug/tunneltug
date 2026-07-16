package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	ExtraEnvFn  func(ns string) map[string]string // service DNS helpers
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
			ExtraEnvFn: func(ns string) map[string]string {
				return map[string]string{
					"WILLIWAW_PUBLIC_URL":     fmt.Sprintf("http://williwaw.%s.svc:%d", ns, 3081),
					"WILLIWAW_SOCIAL_CDN_URL": fmt.Sprintf("http://social.%s.svc:%d", ns, 3085),
					"WILLIWAW_ACK_CHAT_URL":   fmt.Sprintf("http://ack.%s.svc:%d", ns, 3083),
				}
			},
		},
		"motionkb": {
			Name: "motionkb", Display: "MotionKB", Repo: "0trust/motionkb", Port: 8090, Component: "docs-cms",
			Env: map[string]string{
				"MOTIONKB_LISTEN": ":8090",
				"MOTIONKB_DB":     "/data/motionkb.db",
				"MOTIONKB_WAL":    "/data/motionkb.wal",
			},
			ExtraEnvFn: func(ns string) map[string]string {
				return map[string]string{
					"MOTIONKB_PUBLIC_URL":     fmt.Sprintf("http://motionkb.%s.svc:%d", ns, 8090),
					"MOTIONKB_CMS_URL":        fmt.Sprintf("http://motionkb.%s.svc:%d", ns, 8090),
					"MOTIONKB_SOCIAL_CDN_URL": fmt.Sprintf("http://social.%s.svc:%d", ns, 3085),
					"MOTIONKB_WILLIWAW_URL":   fmt.Sprintf("http://williwaw.%s.svc:%d", ns, 3081),
					"MOTIONKB_DEFCON_URL":     fmt.Sprintf("http://ack.%s.svc:%d", ns, 3083),
				}
			},
		},
		"ack": {
			Name: "ack", Display: "Ack", Repo: "0trust/ack", Port: 3083, Component: "event-chat",
			Env: map[string]string{
				"ACK_LISTEN": ":3083",
				"ACK_DB":     "/data/ack.db",
				"ACK_WAL":    "/data/ack.wal",
			},
			ExtraEnvFn: func(ns string) map[string]string {
				return map[string]string{
					"ACK_PUBLIC_URL":     fmt.Sprintf("http://ack.%s.svc:%d", ns, 3083),
					"ACK_SOCIAL_CDN_URL": fmt.Sprintf("http://social.%s.svc:%d", ns, 3085),
				}
			},
		},
		"mail": {
			Name: "mail", Display: "MeshMail", Repo: "0trust/mail", Port: 3086, Component: "mesh-mail",
			Env: map[string]string{
				"MAIL_LISTEN": ":3086",
				"MAIL_DB":     "/data/mail.db",
				"MAIL_WAL":    "/data/mail.wal",
			},
			ExtraEnvFn: func(ns string) map[string]string {
				return map[string]string{
					"MAIL_PUBLIC_URL": fmt.Sprintf("http://mail.%s.svc:%d", ns, 3086),
				}
			},
		},
		"search": {
			Name: "search", Display: "MeshSearch", Repo: "0trust/search", Port: 3087, Component: "mesh-search",
			Env: map[string]string{
				"SEARCH_LISTEN": ":3087",
				"SEARCH_DB":     "/data/search.db",
				"SEARCH_WAL":    "/data/search.wal",
			},
			ExtraEnvFn: func(ns string) map[string]string {
				return map[string]string{
					"SEARCH_PUBLIC_URL": fmt.Sprintf("http://search.%s.svc:%d", ns, 3087),
				}
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
			ExtraEnvFn: func(ns string) map[string]string {
				return map[string]string{
					"SOCIAL_PUBLIC_URL": fmt.Sprintf("http://social.%s.svc:%d", ns, 3085),
				}
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
	}
}

func parseStackProducts(raw string) ([]stackApp, error) {
	raw = strings.TrimSpace(raw)
	cat := stackCatalog()
	var names []string
	if raw == "" || strings.EqualFold(raw, "all") {
		// Default stack for local product barges (exclude platform/services unless asked — heavier).
		names = []string{"social", "ack", "williwaw", "motionkb", "mail", "search"}
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
			names = append(names, p)
		}
	}
	var out []stackApp
	seen := map[string]bool{}
	for _, n := range names {
		app, ok := cat[n]
		if !ok {
			return nil, fmt.Errorf("unknown stack product %q", n)
		}
		if seen[app.Name] {
			continue
		}
		seen[app.Name] = true
		out = append(out, app)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no stack products selected")
	}
	return out, nil
}

func stackImage(app stackApp, tag string) string {
	host := strings.TrimSpace(*hubHost)
	if host == "" {
		host = "hub.tunneltug.com"
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
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

func runStack() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	if err := ensureAuthToken(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	token := strings.TrimSpace(*authToken)
	ns := strings.TrimSpace(*stackNamespace)
	if ns == "" {
		ns = "0trust-stack"
	}
	tag := strings.TrimSpace(*stackTag)
	if tag == "" {
		tag = strings.TrimSpace(*hubTag)
	}
	if tag == "" {
		tag = "dev"
	}

	apps, err := parseStackProducts(*stackProducts)
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
	for _, app := range apps {
		img := stackImage(app, tag)
		if err := ensureK3sBargeImage(ctx, img); err != nil {
			log.Printf("stack pull %s: %v (continuing)", img, err)
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
		if err := ensureStackDeployment(ctx, client, ns, app, img); err != nil {
			return fmt.Errorf("deployment %s: %w", app.Name, err)
		}
		log.Printf("stack reconciled %s image=%s port=%d", app.Name, img, app.Port)
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

func ensureStackDeployment(ctx context.Context, client kubernetes.Interface, ns string, app stackApp, image string) error {
	labels := stackLabels(app)
	replicas := int32(1)
	env := []corev1.EnvVar{}
	for k, v := range app.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	if app.ExtraEnvFn != nil {
		for k, v := range app.ExtraEnvFn(ns) {
			env = append(env, corev1.EnvVar{Name: k, Value: v})
		}
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
					Containers: []corev1.Container{{
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
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
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
		row := map[string]any{
			"name":    app.Name,
			"display": app.Display,
			"image":   stackImage(app, tag),
			"port":    app.Port,
			"ready":   0,
			"desired": 1,
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
