package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Hub is an OCI Distribution-ish registry for TunnelTug barge (k3s) images.
// Pull is public; push requires a cryptographic tunnel token.
// Blobs and manifests are stored on the 0trust.social S3-compatible CDN (or local memory for tests).

type hubConfig struct {
	Listen  string
	Public  string // advertised registry URL, e.g. https://hub.tunneltug.com
	S3URL   string
	Bucket  string
	Token   string
	// When true, use in-memory store (tests / dry-run without S3).
	Memory bool
}

type hubServer struct {
	cfg    hubConfig
	store  hubBlobStore
	mu     sync.Mutex
	upload map[string]*hubUpload
}

type hubUpload struct {
	ID       string
	Name     string
	Data     []byte
	Created  time.Time
}

type hubBlobStore interface {
	Put(key string, data []byte, contentType string) error
	Get(key string) ([]byte, string, error)
	Head(key string) (int64, string, error)
	ListPrefix(prefix string) ([]string, error)
}

// defaultK3sEngineImage is the TunnelTug engine image run inside barge-mode k3s pods.
// Barge = fleet mode (-mode barge -barge-runtime k3s), not a separate product.
// Hub that serves this image is embedded in that same k3s controller.
const defaultK3sEngineImage = "hub.tunneltug.com/tunneltug/engine:latest"

// Deprecated name kept for existing call sites.
const defaultK3sBargeImage = defaultK3sEngineImage

func hubConfigFromFlags() hubConfig {
	cfg := hubConfig{
		Listen: strings.TrimSpace(*hubListen),
		Public: strings.TrimRight(strings.TrimSpace(*hubPublic), "/"),
		S3URL:  strings.TrimRight(strings.TrimSpace(*hubS3URL), "/"),
		Bucket: strings.TrimSpace(*hubBucket),
		Token:  strings.TrimSpace(*authToken),
		Memory: *hubMemory,
	}
	if cfg.Listen == "" {
		cfg.Listen = ":5000"
	}
	if cfg.Public == "" {
		cfg.Public = "https://hub.tunneltug.com"
	}
	if cfg.Bucket == "" {
		cfg.Bucket = "tunneltug-hub"
	}
	if cfg.S3URL == "" {
		cfg.S3URL = "https://0trust.social"
	}
	return cfg
}

func newHubServer(cfg hubConfig) *hubServer {
	var store hubBlobStore
	if cfg.Memory {
		store = newMemoryHubStore()
	} else {
		store = newS3HubStore(cfg.S3URL, cfg.Bucket, cfg.Token)
	}
	return &hubServer{
		cfg:    cfg,
		store:  store,
		upload: make(map[string]*hubUpload),
	}
}

func (h *hubServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", h.handleV2)
	mux.HandleFunc("/v2", h.handleV2Root)
	mux.HandleFunc("/_tunneltug/hub/health", h.handleHealth)
	mux.HandleFunc("/_tunneltug/hub/catalog", h.handleCatalogAPI)
	mux.HandleFunc("/", h.handleIndex)
	return h.withCORS(mux)
}

func (h *hubServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"layer":   "k3s-barge",
		"public":  h.cfg.Public,
		"s3":      h.cfg.S3URL,
		"bucket":  h.cfg.Bucket,
		"pull":    "public",
		"push":    "authenticated",
		"memory":  h.cfg.Memory,
		"image":   defaultK3sBargeImage,
		"license": "MIT",
		"spdx":    "MIT",
		"license_url": "https://github.com/TunnelTug/tunneltug/blob/main/LICENSE",
	})
}

// handleIndex is implemented in hub_index.go (architectures + config builder modal).

// startHubHTTPServer runs the OCI registry. Used by the k3s barge layer (primary)
// and by -mode hub (standalone face only).
func startHubHTTPServer(ctx context.Context, cfg hubConfig) (*http.Server, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("hub requires a cryptographic token")
	}
	h := newHubServer(cfg)
	if cfg.Memory {
		log.Printf("k3s barge hub storage: in-memory (dev)")
	} else {
		log.Printf("k3s barge hub storage: S3 CDN %s/s3/%s", cfg.S3URL, cfg.Bucket)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h.handler(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		log.Printf("k3s barge image hub listening on %s (public %s)", cfg.Listen, cfg.Public)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("k3s barge hub stopped: %v", err)
		}
	}()
	return srv, nil
}

// runHub is a thin standalone entrypoint; production embeds the hub in barge k3s.
func runHub() {
	if err := ensureAuthToken(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	cfg := hubConfigFromFlags()
	cfg.Token = strings.TrimSpace(*authToken)
	ctx, stop := notifyShutdownContext()
	defer stop()
	if _, err := startHubHTTPServer(ctx, cfg); err != nil {
		log.Fatalf("hub: %v", err)
	}
	<-ctx.Done()
}

func (h *hubServer) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, PUT, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Range")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *hubServer) handleV2Root(w http.ResponseWriter, r *http.Request) {
	h.handleV2(w, r)
}

func (h *hubServer) handleV2(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		// GET /v2/ — version check (public)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
		return
	}

	// /v2/<name>/manifests/<ref>
	// /v2/<name>/blobs/<digest>
	// /v2/<name>/blobs/uploads[/uuid]
	// /v2/<name>/tags/list
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		hubError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known")
		return
	}

	// Find the last segment type keyword.
	// Name may contain slashes: tunneltug/barge
	var name, kind, rest string
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "manifests", "blobs", "tags":
			name = strings.Join(parts[:i], "/")
			kind = parts[i]
			rest = strings.Join(parts[i+1:], "/")
			goto routed
		}
	}
	hubError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known")
	return

routed:
	if name == "" || !validRepoName(name) {
		hubError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}

	switch kind {
	case "manifests":
		h.handleManifest(w, r, name, rest)
	case "blobs":
		if strings.HasPrefix(rest, "uploads") {
			h.handleBlobUpload(w, r, name, rest)
			return
		}
		h.handleBlob(w, r, name, rest)
	case "tags":
		if rest == "list" || rest == "" {
			h.handleTagsList(w, r, name)
			return
		}
		hubError(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown tags path")
	default:
		hubError(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown")
	}
}

func (h *hubServer) handleManifest(w http.ResponseWriter, r *http.Request, name, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		hubError(w, http.StatusBadRequest, "MANIFEST_INVALID", "reference required")
		return
	}
	key := hubManifestKey(name, ref)

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		data, ct, err := h.store.Get(key)
		if err != nil {
			hubError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		if ct == "" {
			ct = "application/vnd.docker.distribution.manifest.v2+json"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		sum := sha256.Sum256(data)
		w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case http.MethodPut:
		if !h.requirePushAuth(w, r) {
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			hubError(w, http.StatusBadRequest, "MANIFEST_INVALID", "read failed")
			return
		}
		if len(data) == 0 {
			hubError(w, http.StatusBadRequest, "MANIFEST_INVALID", "empty manifest")
			return
		}
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/vnd.docker.distribution.manifest.v2+json"
		}
		if err := h.store.Put(key, data, ct); err != nil {
			log.Printf("hub manifest put: %v", err)
			hubError(w, http.StatusBadGateway, "DENIED", "storage error")
			return
		}
		// Also index by digest for immutable pulls.
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		_ = h.store.Put(hubManifestKey(name, digest), data, ct)
		// Tag index for tags/list
		_ = h.indexTag(name, ref)

		w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, ref))
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if !h.requirePushAuth(w, r) {
			return
		}
		// Soft-delete: write empty tombstone not supported on all stores; return 202.
		w.WriteHeader(http.StatusAccepted)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *hubServer) handleBlob(w http.ResponseWriter, r *http.Request, name, digest string) {
	digest = strings.TrimSpace(digest)
	if !validDigest(digest) {
		hubError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}
	key := hubBlobKey(digest)

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if r.Method == http.MethodHead {
			size, ct, err := h.store.Head(key)
			if err != nil {
				hubError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
				return
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
			return
		}
		data, ct, err := h.store.Get(key)
		if err != nil {
			hubError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
			return
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *hubServer) handleBlobUpload(w http.ResponseWriter, r *http.Request, name, rest string) {
	// rest is "uploads" or "uploads/<uuid>"
	rest = strings.TrimPrefix(rest, "uploads")
	rest = strings.TrimPrefix(rest, "/")

	switch r.Method {
	case http.MethodPost:
		// Start upload
		if !h.requirePushAuth(w, r) {
			return
		}
		// Monolithic upload: POST with ?digest= and body
		if dig := strings.TrimSpace(r.URL.Query().Get("digest")); dig != "" {
			data, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
			if err != nil {
				hubError(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", "read failed")
				return
			}
			if err := h.commitBlob(dig, data); err != nil {
				hubError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
				return
			}
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, dig))
			w.Header().Set("Docker-Content-Digest", dig)
			w.WriteHeader(http.StatusCreated)
			return
		}
		id, err := GenerateSecureToken()
		if err != nil {
			hubError(w, http.StatusInternalServerError, "DENIED", "uuid failed")
			return
		}
		id = id[:32]
		h.mu.Lock()
		h.upload[id] = &hubUpload{ID: id, Name: name, Created: time.Now().UTC()}
		h.mu.Unlock()
		loc := fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id)
		w.Header().Set("Location", loc)
		w.Header().Set("Docker-Upload-UUID", id)
		w.Header().Set("Range", "0-0")
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPatch:
		if !h.requirePushAuth(w, r) {
			return
		}
		id := rest
		h.mu.Lock()
		up, ok := h.upload[id]
		h.mu.Unlock()
		if !ok {
			hubError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload unknown")
			return
		}
		chunk, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
		if err != nil {
			hubError(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", "read failed")
			return
		}
		h.mu.Lock()
		up.Data = append(up.Data, chunk...)
		n := len(up.Data)
		h.mu.Unlock()
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id))
		w.Header().Set("Docker-Upload-UUID", id)
		w.Header().Set("Range", fmt.Sprintf("0-%d", n-1))
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPut:
		if !h.requirePushAuth(w, r) {
			return
		}
		id := rest
		dig := strings.TrimSpace(r.URL.Query().Get("digest"))
		if dig == "" {
			hubError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest required")
			return
		}
		h.mu.Lock()
		up, ok := h.upload[id]
		if ok {
			// Final chunk may be in body.
			if r.ContentLength != 0 {
				chunk, _ := io.ReadAll(io.LimitReader(r.Body, 512<<20))
				up.Data = append(up.Data, chunk...)
			}
			data := up.Data
			delete(h.upload, id)
			h.mu.Unlock()
			if err := h.commitBlob(dig, data); err != nil {
				hubError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
				return
			}
		} else {
			h.mu.Unlock()
			// Complete with body only
			data, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
			if err != nil {
				hubError(w, http.StatusBadRequest, "BLOB_UPLOAD_INVALID", "read failed")
				return
			}
			if err := h.commitBlob(dig, data); err != nil {
				hubError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
				return
			}
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, dig))
		w.Header().Set("Docker-Content-Digest", dig)
		w.WriteHeader(http.StatusCreated)

	case http.MethodGet:
		// Upload status
		id := rest
		h.mu.Lock()
		up, ok := h.upload[id]
		h.mu.Unlock()
		if !ok {
			hubError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload unknown")
			return
		}
		n := len(up.Data)
		w.Header().Set("Docker-Upload-UUID", id)
		if n == 0 {
			w.Header().Set("Range", "0-0")
		} else {
			w.Header().Set("Range", fmt.Sprintf("0-%d", n-1))
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *hubServer) commitBlob(digest string, data []byte) error {
	if !validDigest(digest) {
		return fmt.Errorf("invalid digest")
	}
	sum := sha256.Sum256(data)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, digest) {
		return fmt.Errorf("digest mismatch")
	}
	return h.store.Put(hubBlobKey(digest), data, "application/octet-stream")
}

func (h *hubServer) handleTagsList(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tags := h.listTags(name)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"name": name,
		"tags": tags,
	})
}

func (h *hubServer) indexTag(name, ref string) error {
	if strings.HasPrefix(ref, "sha256:") {
		return nil
	}
	key := hubTagsKey(name)
	var tags []string
	if data, _, err := h.store.Get(key); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &tags)
	}
	found := false
	for _, t := range tags {
		if t == ref {
			found = true
			break
		}
	}
	if !found {
		tags = append(tags, ref)
	}
	raw, _ := json.Marshal(tags)
	return h.store.Put(key, raw, "application/json")
}

func (h *hubServer) listTags(name string) []string {
	key := hubTagsKey(name)
	data, _, err := h.store.Get(key)
	if err != nil || len(data) == 0 {
		return []string{}
	}
	var tags []string
	if json.Unmarshal(data, &tags) != nil {
		return []string{}
	}
	return tags
}

// requirePushAuth enforces authenticated write. Pull paths never call this.
// Accepts: Authorization Bearer <token> or Basic (any user, password=token).
func (h *hubServer) requirePushAuth(w http.ResponseWriter, r *http.Request) bool {
	expected := strings.TrimSpace(h.cfg.Token)
	if expected == "" {
		hubError(w, http.StatusUnauthorized, "UNAUTHORIZED", "hub push token not configured")
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="tunneltug-hub",service="hub.tunneltug.com"`)
		hubError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return false
	}
	var provided string
	switch {
	case strings.HasPrefix(strings.ToLower(auth), "bearer "):
		provided = strings.TrimSpace(auth[7:])
	case strings.HasPrefix(strings.ToLower(auth), "basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[6:]))
		if err != nil {
			hubError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid basic auth")
			return false
		}
		// user:password — password is the token
		parts := strings.SplitN(string(raw), ":", 2)
		if len(parts) == 2 {
			provided = parts[1]
		} else {
			provided = string(raw)
		}
	default:
		hubError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unsupported auth scheme")
		return false
	}
	if !tokensEqual(provided, expected) {
		hubError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
		return false
	}
	return true
}

func hubError(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"errors": []map[string]string{{"code": errCode, "message": msg}},
	})
}

func hubBlobKey(digest string) string {
	digest = strings.ToLower(strings.TrimSpace(digest))
	digest = strings.ReplaceAll(digest, ":", "/")
	return "blobs/" + digest
}

func hubManifestKey(name, ref string) string {
	return "manifests/" + name + "/" + ref
}

func hubTagsKey(name string) string {
	return "tags/" + name + ".json"
}

func validRepoName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	// Disallow path traversal and absolute-looking names.
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, ".") {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if p == "" || p == "." || p == ".." || len(p) > 128 {
			return false
		}
		if p[0] == '.' || p[0] == '-' {
			return false
		}
		for _, c := range p {
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
				continue
			}
			return false
		}
	}
	return true
}

func validDigest(d string) bool {
	d = strings.ToLower(strings.TrimSpace(d))
	if !strings.HasPrefix(d, "sha256:") {
		return false
	}
	hexPart := d[7:]
	if len(hexPart) != 64 {
		return false
	}
	for _, c := range hexPart {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}
