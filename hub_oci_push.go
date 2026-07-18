package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Pack a single linux binary into a minimal Docker v2 image and push it to the
// TunnelTug hub over the Registry HTTP API. Used by -mode hub-publish when the
// image is not already in local k3s/containerd (no docker/crane required).

type ociBlob struct {
	data   []byte
	digest string // sha256:hex
	size   int64
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newBlob(b []byte) ociBlob {
	return ociBlob{data: b, digest: digestOf(b), size: int64(len(b))}
}

// packBinaryImage builds config + gzip layer + manifest for a distroless-style
// image whose only payload is the product binary at /usr/local/bin/product.
func packBinaryImage(binPath string) (config, layer ociBlob, manifest []byte, err error) {
	raw, err := os.ReadFile(binPath)
	if err != nil {
		return ociBlob{}, ociBlob{}, nil, fmt.Errorf("read binary: %w", err)
	}

	// Layer: gzip(tar) with /usr/local/bin/product (0755)
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	// Parent dirs (optional for containerd; helps some runtimes)
	for _, dir := range []struct {
		name string
		mode int64
	}{
		{"usr", 0755},
		{"usr/local", 0755},
		{"usr/local/bin", 0755},
		{"data", 0755},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name:     dir.name + "/",
			Typeflag: tar.TypeDir,
			Mode:     dir.mode,
			ModTime:  time.Unix(0, 0).UTC(),
		}); err != nil {
			return ociBlob{}, ociBlob{}, nil, err
		}
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     "usr/local/bin/product",
		Typeflag: tar.TypeReg,
		Mode:     0755,
		Size:     int64(len(raw)),
		ModTime:  time.Unix(0, 0).UTC(),
	}); err != nil {
		return ociBlob{}, ociBlob{}, nil, err
	}
	if _, err := tw.Write(raw); err != nil {
		return ociBlob{}, ociBlob{}, nil, err
	}
	if err := tw.Close(); err != nil {
		return ociBlob{}, ociBlob{}, nil, err
	}

	var gzBuf bytes.Buffer
	gzw := gzip.NewWriter(&gzBuf)
	if _, err := gzw.Write(tarBuf.Bytes()); err != nil {
		return ociBlob{}, ociBlob{}, nil, err
	}
	if err := gzw.Close(); err != nil {
		return ociBlob{}, ociBlob{}, nil, err
	}
	layer = newBlob(gzBuf.Bytes())

	// DiffID is sha256 of uncompressed tar (Docker image config)
	diffID := digestOf(tarBuf.Bytes())

	cfgObj := map[string]interface{}{
		"architecture": "amd64",
		"os":           "linux",
		"config": map[string]interface{}{
			"Entrypoint": []string{"/usr/local/bin/product"},
			"WorkingDir": "/data",
			"User":       "65532:65532",
		},
		"rootfs": map[string]interface{}{
			"type":     "layers",
			"diff_ids": []string{diffID},
		},
		"history": []map[string]interface{}{
			{"created_by": "tunneltug hub-publish", "comment": "single-binary product image"},
		},
	}
	cfgJSON, err := json.Marshal(cfgObj)
	if err != nil {
		return ociBlob{}, ociBlob{}, nil, err
	}
	config = newBlob(cfgJSON)

	manObj := map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.docker.distribution.manifest.v2+json",
		"config": map[string]interface{}{
			"mediaType": "application/vnd.docker.container.image.v1+json",
			"size":      config.size,
			"digest":    config.digest,
		},
		"layers": []map[string]interface{}{
			{
				"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
				"size":      layer.size,
				"digest":    layer.digest,
			},
		},
	}
	manifest, err = json.Marshal(manObj)
	if err != nil {
		return ociBlob{}, ociBlob{}, nil, err
	}
	return config, layer, manifest, nil
}

// pushOCIToHub uploads config, layer, and manifest to the TunnelTug registry.
// host is registry host without scheme (hub.tunneltug.com). repo is 0trust/name.
func pushOCIToHub(baseURL, repo, tag, token string, config, layer ociBlob, manifest []byte) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	repo = strings.Trim(repo, "/")
	tag = strings.TrimSpace(tag)
	if tag == "" {
		tag = "latest"
	}
	if token == "" {
		return fmt.Errorf("token required for hub push")
	}
	client := &http.Client{Timeout: 15 * time.Minute}

	for _, b := range []ociBlob{config, layer} {
		if err := hubPutBlob(client, baseURL, repo, token, b); err != nil {
			return fmt.Errorf("blob %s: %w", b.digest, err)
		}
	}

	url := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, repo, tag)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(manifest))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("manifest put %s: %s %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func hubPutBlob(client *http.Client, baseURL, repo, token string, b ociBlob) error {
	// Monolithic upload: POST .../blobs/uploads/?digest=sha256:... with body
	url := fmt.Sprintf("%s/v2/%s/blobs/uploads/?digest=%s", baseURL, repo, b.digest)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b.data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = b.size
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusAccepted {
		return nil
	}
	// Some registries return 201 only after PATCH+PUT; retry two-step if needed.
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized: %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		// Fall through to session upload
		_ = body
	} else {
		return nil
	}

	// Start upload session
	startURL := fmt.Sprintf("%s/v2/%s/blobs/uploads/", baseURL, repo)
	req, err = http.NewRequest(http.MethodPost, startURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp2, err := client.Do(req)
	if err != nil {
		return err
	}
	loc := resp2.Header.Get("Location")
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted && resp2.StatusCode != http.StatusCreated {
		return fmt.Errorf("start upload: %s (monolithic was %s)", resp2.Status, resp.Status)
	}
	if loc == "" {
		return fmt.Errorf("start upload: missing Location")
	}
	if strings.HasPrefix(loc, "/") {
		// relative
		u := strings.TrimPrefix(baseURL, "https://")
		u = strings.TrimPrefix(u, "http://")
		// baseURL already has scheme
		loc = baseURL + loc
	} else if !strings.HasPrefix(loc, "http") {
		loc = baseURL + "/" + strings.TrimPrefix(loc, "/")
	}
	// Complete with PUT ?digest=
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	putURL := loc + sep + "digest=" + b.digest
	req, err = http.NewRequest(http.MethodPut, putURL, bytes.NewReader(b.data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = b.size
	resp3, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp3.Body.Close()
	body3, _ := io.ReadAll(io.LimitReader(resp3.Body, 4096))
	if resp3.StatusCode != http.StatusCreated && resp3.StatusCode != http.StatusOK {
		return fmt.Errorf("blob put: %s %s", resp3.Status, strings.TrimSpace(string(body3)))
	}
	return nil
}

// resolveHubBinary finds a linux product binary for hub-publish packing.
// Checks -hub-dist/<name>/product and common 0TrustCloud deploy/oci/dist paths.
func resolveHubBinary(productName string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(productName))
	// Map stack key → dist folder name
	dirName := name
	if name == "dbsc-relay" {
		dirName = "dbsc_relay"
	}

	var candidates []string
	if d := strings.TrimSpace(*hubDist); d != "" {
		candidates = append(candidates,
			filepath.Join(d, dirName, "product"),
			filepath.Join(d, name, "product"),
			filepath.Join(d, dirName),
		)
	}
	// Common layouts relative to cwd / home
	home, _ := os.UserHomeDir()
	for _, root := range []string{
		filepath.Join("deploy", "oci", "dist"),
		filepath.Join("..", "0TrustCloud", "deploy", "oci", "dist"),
		filepath.Join(home, "0TrustCloud", "deploy", "oci", "dist"),
		filepath.Join(home, "tunneltug", "..", "0TrustCloud", "deploy", "oci", "dist"),
	} {
		candidates = append(candidates,
			filepath.Join(root, dirName, "product"),
			filepath.Join(root, name, "product"),
		)
	}
	// Engine / anycast / kernel storage barges are the tunneltug binary.
	if name == "tunneltug" || name == "anycast" || name == "engine" ||
		name == "ultimate_db" || name == "ultimate-db" ||
		name == "ultimate_keystore" || name == "ultimate-keystore" {
		candidates = append(candidates,
			filepath.Join("bin", "tunneltug"),
			filepath.Join("bin", "tunneltug.exe"),
			filepath.Join("bin", "anycast", "product"),
			filepath.Join(home, "tunneltug", "bin", "tunneltug"),
			filepath.Join(home, "tunneltug", "bin", "tunneltug.exe"),
		)
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() && st.Size() > 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("no binary for %q (set -hub-dist to deploy/oci/dist)", productName)
}

func hubBaseURL(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimRight(host, "/")
	// Production hub is HTTPS
	if host == "hub.tunneltug.com" || strings.Contains(host, ".") {
		return "https://" + host
	}
	return "http://" + host
}

// publishBinaryToHub packs a product binary and pushes it via the hub Registry API.
func publishBinaryToHub(binPath, host, repo, tag, token string) error {
	config, layer, manifest, err := packBinaryImage(binPath)
	if err != nil {
		return err
	}
	base := hubBaseURL(host)
	return pushOCIToHub(base, repo, tag, token, config, layer, manifest)
}
