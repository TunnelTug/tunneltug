package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type bargeState string

const (
	bargeStarting bargeState = "starting"
	bargeRunning  bargeState = "running"
	bargeStopped  bargeState = "stopped"
)

type bargeInstance struct {
	id          int
	controlPort string
	publicPort  string
	dashPort    string

	mu       sync.Mutex
	state    bargeState
	restarts int
	pid      int
	lastExit error
	started  time.Time
	cmd      *exec.Cmd
}

type BargeFleet struct {
	service   string
	host      string
	replicas  int
	step      int
	registrar *bargeLBRegistrar

	mu     sync.RWMutex
	barges []*bargeInstance
}

func newBargeFleet() (*BargeFleet, error) {
	service := strings.ToLower(strings.TrimSpace(*bargeService))
	if service != "server" && service != "client" {
		return nil, fmt.Errorf("invalid -barge-service %q: use server or client", *bargeService)
	}

	replicas := *bargeReplicas
	if replicas < 1 {
		return nil, fmt.Errorf("-barge-replicas must be at least 1")
	}

	step := *bargePortStep
	if step < 1 {
		return nil, fmt.Errorf("-barge-port-step must be at least 1")
	}

	if err := validateBargePortRange(*controlPort, replicas, step, "control"); err != nil {
		return nil, err
	}
	if service == "server" {
		if err := validateBargePortRange(*publicPort, replicas, step, "public"); err != nil {
			return nil, err
		}
	}
	if service == "client" {
		if err := validateBargePortRange(*dashPort, replicas, step, "dash"); err != nil {
			return nil, err
		}
	}

	baseControl, _ := strconv.Atoi(*controlPort)
	basePublic, _ := strconv.Atoi(*publicPort)
	baseDash, _ := strconv.Atoi(*dashPort)

	fleet := &BargeFleet{
		service:  service,
		host:     strings.TrimSpace(*bargeHost),
		replicas: replicas,
		step:     step,
		barges:   make([]*bargeInstance, replicas),
	}

	for i := 0; i < replicas; i++ {
		fleet.barges[i] = &bargeInstance{
			id:          i,
			controlPort: strconv.Itoa(baseControl + i*step),
			publicPort:  strconv.Itoa(basePublic + i*step),
			dashPort:    strconv.Itoa(baseDash + i*step),
			state:       bargeStopped,
		}
	}

	return fleet, nil
}

func validateBargePortRange(base string, replicas, step int, name string) error {
	start, err := strconv.Atoi(base)
	if err != nil {
		return fmt.Errorf("invalid base %s port %q", name, base)
	}
	end := start + (replicas-1)*step
	if end > 65535 {
		return fmt.Errorf("%s port range %d-%d exceeds max port 65535 (%d replicas, step %d)", name, start, end, replicas, step)
	}
	return nil
}

func (f *BargeFleet) backendSpec() string {
	if f.service != "server" {
		return ""
	}
	parts := make([]string, len(f.barges))
	for i, b := range f.barges {
		parts[i] = net.JoinHostPort(f.host, b.controlPort) + ":" + b.publicPort
	}
	return strings.Join(parts, ",")
}

func (f *BargeFleet) snapshot() []map[string]any {
	f.mu.RLock()
	defer f.mu.RUnlock()

	out := make([]map[string]any, len(f.barges))
	for i, b := range f.barges {
		b.mu.Lock()
		entry := map[string]any{
			"id":       b.id,
			"state":    b.state,
			"restarts": b.restarts,
			"pid":      b.pid,
		}
		if f.service == "server" {
			entry["control"] = net.JoinHostPort(f.host, b.controlPort)
			entry["public"] = net.JoinHostPort(f.host, b.publicPort)
		} else {
			entry["dash"] = "127.0.0.1:" + b.dashPort
		}
		if b.lastExit != nil {
			entry["last_exit"] = b.lastExit.Error()
		}
		if !b.started.IsZero() {
			entry["uptime_sec"] = int(time.Since(b.started).Seconds())
		}
		b.mu.Unlock()
		out[i] = entry
	}
	return out
}

func bargeRuntimeMode() string {
	return strings.ToLower(strings.TrimSpace(*bargeRuntime))
}

func runBarge() {
	switch bargeRuntimeMode() {
	case "k3s", "":
		runBargeK3s()
	case "process":
		runBargeProcess()
	default:
		log.Fatalf("Barge configuration error: invalid -barge-runtime %q (use k3s or process)", *bargeRuntime)
	}
}

func runBargeProcess() {
	ctx, stop := notifyShutdownContext()
	defer stop()

	fleet, err := newBargeFleet()
	if err != nil {
		log.Fatalf("Barge configuration error: %v", err)
	}

	if lbAddr := strings.TrimSpace(*bargeLB); lbAddr != "" {
		if fleet.service != "server" {
			log.Fatalf("Barge configuration error: -barge-lb requires -barge-service server")
		}
		fleet.registrar, err = newBargeLBRegistrar(lbAddr)
		if err != nil {
			log.Fatalf("Barge configuration error: %v", err)
		}
	}

	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to resolve executable: %v", err)
	}

	log.Printf("Starting barge fleet (process): %d %s replica(s), step %d", fleet.replicas, fleet.service, fleet.step)
	if fleet.registrar != nil {
		log.Printf("Automatic LB registration enabled via %s", strings.TrimSpace(*bargeLB))
	} else if spec := fleet.backendSpec(); spec != "" {
		log.Printf("LB backends (use with -mode lb -backends): %s", spec)
	}

	if !*quiet {
		go runBargeDashboard(ctx, fleet)
	}

	var wg sync.WaitGroup
	for _, b := range fleet.barges {
		wg.Add(1)
		go func(inst *bargeInstance) {
			defer wg.Done()
			fleet.superviseBarge(ctx, exe, inst)
		}(b)
	}

	<-ctx.Done()
	wg.Wait()
	if fleet.registrar != nil {
		fleet.deregisterAll(context.Background())
	}
	log.Println("Barge fleet stopped")
}

func (f *BargeFleet) deregisterAll(ctx context.Context) {
	for _, b := range f.barges {
		b.mu.Lock()
		running := b.state == bargeRunning
		b.mu.Unlock()
		if running {
			_ = f.registrar.deregister(ctx, f.registrar.endpointFromBarge(f.host, b))
		}
	}
}

func (f *BargeFleet) superviseBarge(ctx context.Context, exe string, b *bargeInstance) {
	delay := time.Duration(*bargeRestartDelay) * time.Second

	for ctx.Err() == nil {
		if err := f.startBarge(ctx, exe, b); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Barge %d failed to start: %v", b.id, err)
		} else {
			regCtx, regCancel := context.WithCancel(ctx)
			ep := lbEndpoint{}
			if f.registrar != nil {
				ep = f.registrar.endpointFromBarge(f.host, b)
				if err := f.registrar.waitReady(regCtx, ep); err != nil {
					log.Printf("Barge %d not ready for LB registration: %v", b.id, err)
				} else if err := f.registrar.register(regCtx, ep); err != nil {
					log.Printf("Barge %d LB registration failed: %v", b.id, err)
				} else {
					log.Printf("Barge %d registered with LB", b.id)
					go f.registrar.heartbeat(regCtx, ep)
				}
			}

			if err := f.waitBarge(ctx, b); err != nil && ctx.Err() == nil {
				log.Printf("Barge %d wait error: %v", b.id, err)
			}
			regCancel()
			if f.registrar != nil {
				if err := f.registrar.deregister(context.Background(), ep); err != nil && !*quiet {
					log.Printf("Barge %d LB deregistration failed: %v", b.id, err)
				}
			}
		}

		if ctx.Err() != nil {
			return
		}

		b.mu.Lock()
		exitErr := b.lastExit
		restarts := b.restarts
		b.mu.Unlock()

		if *bargeMaxRestarts > 0 && restarts >= *bargeMaxRestarts {
			log.Printf("Barge %d reached max restarts (%d), not restarting", b.id, *bargeMaxRestarts)
			return
		}

		if exitErr != nil {
			log.Printf("Barge %d exited: %v. Restarting in %s...", b.id, exitErr, delay)
		} else {
			log.Printf("Barge %d exited cleanly. Restarting in %s...", b.id, delay)
		}

		if !sleepOrDone(ctx, delay) {
			return
		}
	}
}

func (f *BargeFleet) startBarge(ctx context.Context, exe string, b *bargeInstance) error {
	args := f.buildChildArgs(b)
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	b.mu.Lock()
	b.state = bargeStarting
	b.cmd = cmd
	b.mu.Unlock()

	if err := cmd.Start(); err != nil {
		b.mu.Lock()
		b.state = bargeStopped
		b.lastExit = err
		b.mu.Unlock()
		return err
	}

	b.mu.Lock()
	b.state = bargeRunning
	b.pid = cmd.Process.Pid
	b.started = time.Now()
	b.restarts++
	b.lastExit = nil
	b.mu.Unlock()

	log.Printf("Barge %d started (pid %d, %s)", b.id, b.pid, f.bargeLabel(b))
	return nil
}

func (f *BargeFleet) waitBarge(ctx context.Context, b *bargeInstance) error {
	b.mu.Lock()
	cmd := b.cmd
	b.mu.Unlock()
	if cmd == nil {
		return fmt.Errorf("no process")
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// Prefer graceful stop so the child can write a barge snapshot before exit.
		signalGracefulStop(cmd)
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		return ctx.Err()
	case err := <-done:
		b.mu.Lock()
		b.state = bargeStopped
		b.pid = 0
		b.lastExit = err
		b.cmd = nil
		b.mu.Unlock()
		return err
	}
}

func (f *BargeFleet) bargeLabel(b *bargeInstance) string {
	if f.service == "server" {
		return fmt.Sprintf("control %s, public %s", net.JoinHostPort(f.host, b.controlPort), net.JoinHostPort(f.host, b.publicPort))
	}
	return fmt.Sprintf("dash 127.0.0.1:%s", b.dashPort)
}

func (f *BargeFleet) buildChildArgs(b *bargeInstance) []string {
	args := []string{
		"-mode", f.service,
		"-token", strings.TrimSpace(*authToken),
		"-routing", strings.TrimSpace(*routing),
		"-namespace", normalizeNamespace(*namespace),
		"-control", b.controlPort,
		"-keepalive", strconv.Itoa(*keepAlive),
		"-buffer", strconv.Itoa(*streamBuffer),
		"-maxstreams", strconv.Itoa(*maxStreams),
		"-quiet",
	}

	if f.service == "server" {
		args = append(args, "-public", b.publicPort)
		if dir := strings.TrimSpace(*snapshotDir); dir != "" {
			// Per-replica snapshot files share the dir; identity key includes control port.
			args = append(args, "-snapshot-dir", dir, "-snapshot-on-shutdown=true", "-snapshot-restore=true")
		}
	} else {
		args = append(args,
			"-server", strings.TrimSpace(*serverIP),
			"-local", strings.TrimSpace(*localPort),
			"-subdomain", strings.TrimSpace(*subdomain),
			"-dash", b.dashPort,
		)
		if *insecure {
			args = append(args, "-insecure")
		}
	}

	if *prod {
		args = append(args, "-prod")
	}
	if *dev {
		args = append(args, "-dev")
	}
	if domain := strings.TrimSpace(*domain); domain != "" {
		args = append(args, "-domain", domain)
	}
	if email := strings.TrimSpace(*email); email != "" {
		args = append(args, "-email", email)
	}
	if subalt := strings.TrimSpace(*subalt); subalt != "" {
		args = append(args, "-subalt", subalt)
	}
	if cache := strings.TrimSpace(*acmeCache); cache != "" && cache != "certs-cache" {
		args = append(args, "-acme-cache", cache)
	}
	if !*acmeHTTP {
		args = append(args, "-acme-http=false")
	}
	if !*http3Enabled {
		args = append(args, "-http3=false")
	}
	if cert := strings.TrimSpace(*certFile); cert != "" {
		args = append(args, "-cert", cert)
	}
	if key := strings.TrimSpace(*keyFile); key != "" {
		args = append(args, "-key", key)
	}

	return args
}

func runBargeDashboard(ctx context.Context, fleet *BargeFleet) {
	mux := http.NewServeMux()

	mux.HandleFunc("/_tunneltug/barges", func(w http.ResponseWriter, r *http.Request) {
		running := 0
		fleet.mu.RLock()
		for _, b := range fleet.barges {
			b.mu.Lock()
			if b.state == bargeRunning {
				running++
			}
			b.mu.Unlock()
		}
		fleet.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{
			"status":   "ok",
			"mode":     "barge",
			"runtime":  "process",
			"service":  fleet.service,
			"replicas": fleet.replicas,
			"running":  running,
			"barges":   fleet.snapshot(),
		}
		if fleet.registrar != nil {
			payload["lb_registration"] = strings.TrimSpace(*bargeLB)
		} else if spec := fleet.backendSpec(); spec != "" {
			payload["lb_backends"] = spec
		}
		_ = json.NewEncoder(w).Encode(payload)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h2>TunnelTug Barge Fleet</h2><p>Service: %s | Replicas: %d | <a href="/_tunneltug/barges">JSON status</a></p>`,
			fleet.service, fleet.replicas)
		if fleet.registrar != nil {
			fmt.Fprintf(w, `<p>LB auto-registration: <code>%s</code></p>`, strings.TrimSpace(*bargeLB))
		} else if spec := fleet.backendSpec(); spec != "" {
			fmt.Fprintf(w, `<p>LB backends: <code>%s</code></p>`, spec)
		}
		fmt.Fprint(w, `<table border="1" cellpadding="6"><tr><th>ID</th><th>State</th><th>PID</th><th>Restarts</th><th>Endpoints</th></tr>`)
		for _, entry := range fleet.snapshot() {
			endpoints := ""
			if fleet.service == "server" {
				endpoints = fmt.Sprintf("%s / %s", entry["control"], entry["public"])
			} else {
				endpoints = fmt.Sprintf("%v", entry["dash"])
			}
			fmt.Fprintf(w, `<tr><td>%v</td><td>%v</td><td>%v</td><td>%v</td><td>%s</td></tr>`,
				entry["id"], entry["state"], entry["pid"], entry["restarts"], endpoints)
		}
		fmt.Fprint(w, `</table></body></html>`)
	})

	addr := "127.0.0.1:" + *bargeDashPort
	log.Printf("Barge fleet dashboard at http://%s", addr)

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
		log.Printf("Barge dashboard stopped: %v", err)
	}
}

// bargeScalingProfile applies vertical scaling multipliers before child processes start.
func bargeScalingProfile() {
	if *bargeBufferScale > 1 {
		scaled := *streamBuffer * *bargeBufferScale
		if scaled > maxStreamBuffer {
			scaled = maxStreamBuffer
		}
		*streamBuffer = scaled
	}
	if *bargeStreamScale > 1 && *maxStreams > 0 {
		*maxStreams = *maxStreams * *bargeStreamScale
	}
}

