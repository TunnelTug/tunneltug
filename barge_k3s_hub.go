package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Hub + engine image ops are part of TunnelTug's k3s fleet mode ("barge").
//
// Barge means: tunneltug runs k3s (StatefulSet of tunnel server pods).
// It is not a product next to MeshMail/Platform — it is how TunnelTug operates k3s.
//
// When -mode barge -barge-runtime k3s runs:
//  1. OCI registry (hub) starts (public pull, auth push → 0trust.social S3)
//  2. Local k3s pulls the *engine* image (tunneltug binary) via k3s ctr
//  3. StatefulSet is reconciled so each pod runs that engine image

func k3sHubEnabled() bool {
	return *k3sHub
}

func k3sHubPullEnabled() bool {
	return *k3sHubPull
}

// startK3sBargeHub starts the image hub bound into the k3s barge controller.
func startK3sBargeHub(ctx context.Context, token string) (*hubServer, error) {
	if !k3sHubEnabled() {
		return nil, nil
	}
	cfg := hubConfigFromFlags()
	cfg.Token = strings.TrimSpace(token)
	if cfg.Token == "" {
		return nil, fmt.Errorf("k3s barge hub requires cryptographic token")
	}
	if _, err := startHubHTTPServer(ctx, cfg); err != nil {
		return nil, err
	}
	// Lightweight handle for status logging (HTTP server owns the live store).
	return newHubServer(cfg), nil
}

// ensureK3sBargeImage pulls image into the local k3s/containerd store so pods can start
// without an external Docker daemon. Public hub pulls need no credentials.
func ensureK3sBargeImage(ctx context.Context, image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return fmt.Errorf("empty barge image")
	}
	if !k3sHubPullEnabled() {
		return nil
	}

	// Already present?
	if k3sCtrImageExists(ctx, image) {
		log.Printf("k3s barge image already present: %s", image)
		return nil
	}

	log.Printf("k3s barge hub: pulling image into local k3s: %s", image)
	if err := k3sCtrImagesPull(ctx, image); err != nil {
		return fmt.Errorf("k3s ctr pull %s: %w (is k3s installed and running?)", image, err)
	}
	log.Printf("k3s barge hub: image ready: %s", image)
	return nil
}

// publishK3sBargeImage pushes a local containerd image to the barge hub (auth required).
// Called when -k3s-hub-publish is set (source image ref).
func publishK3sBargeImage(ctx context.Context, source, dest, token string) error {
	source = strings.TrimSpace(source)
	dest = strings.TrimSpace(dest)
	token = strings.TrimSpace(token)
	if source == "" || dest == "" {
		return fmt.Errorf("publish requires source and destination image")
	}
	if token == "" {
		return fmt.Errorf("publish requires cryptographic token")
	}
	if source != dest {
		if err := k3sCtrImagesTag(ctx, source, dest); err != nil {
			return fmt.Errorf("k3s ctr tag: %w", err)
		}
	}
	log.Printf("k3s barge hub: pushing %s (authenticated)", dest)
	if err := k3sCtrImagesPush(ctx, dest, "tunneltug", token); err != nil {
		return fmt.Errorf("k3s ctr push: %w", err)
	}
	log.Printf("k3s barge hub: published %s", dest)
	return nil
}

func k3sCtrImageExists(ctx context.Context, image string) bool {
	out, err := runK3sCtr(ctx, nil, "images", "list", "-q")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == image || strings.HasPrefix(line, image+"@") {
			return true
		}
	}
	return false
}

func k3sCtrImagesPull(ctx context.Context, image string) error {
	_, err := runK3sCtr(ctx, nil, "images", "pull", image)
	return err
}

func k3sCtrImagesTag(ctx context.Context, source, dest string) error {
	_, err := runK3sCtr(ctx, nil, "images", "tag", source, dest)
	return err
}

func k3sCtrImagesPush(ctx context.Context, image, user, token string) error {
	auth := user + ":" + token
	_, err := runK3sCtr(ctx, nil, "images", "push", "--user", auth, image)
	return err
}

// runK3sCtr runs containerd image ops against the k3s cluster.
// Tries `k3s ctr` first, then ctr on the k3s socket (k8s.io namespace).
func runK3sCtr(ctx context.Context, extraEnv []string, args ...string) ([]byte, error) {
	type attempt struct {
		bin  string
		argv []string
	}
	attempts := []attempt{
		{bin: "k3s", argv: append([]string{"ctr"}, args...)},
		{bin: "ctr", argv: append([]string{
			"--address", k3sContainerdSock(),
			"-n", "k8s.io",
		}, args...)},
	}

	var last error
	for _, a := range attempts {
		if _, err := exec.LookPath(a.bin); err != nil {
			last = err
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		cmd := exec.CommandContext(cctx, a.bin, a.argv...)
		cmd.Env = append(os.Environ(), extraEnv...)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			return out, nil
		}
		last = fmt.Errorf("%s %s: %w\n%s", a.bin, strings.Join(a.argv, " "), err, strings.TrimSpace(string(out)))
	}
	if last == nil {
		last = fmt.Errorf("k3s/ctr not found on PATH")
	}
	return nil, last
}

func k3sContainerdSock() string {
	if v := strings.TrimSpace(os.Getenv("CONTAINERD_ADDRESS")); v != "" {
		return v
	}
	// Default k3s containerd socket.
	return "/run/k3s/containerd/containerd.sock"
}

func defaultBargeImageRef() string {
	if img := strings.TrimSpace(*k3sImage); img != "" {
		return img
	}
	return defaultK3sBargeImage
}
