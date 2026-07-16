package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testHub(t *testing.T) *hubServer {
	t.Helper()
	tok, err := GenerateSecureToken()
	if err != nil {
		t.Fatal(err)
	}
	return &hubServer{
		cfg: hubConfig{
			Public: "https://hub.tunneltug.com",
			Token:  tok,
			Memory: true,
		},
		store:  newMemoryHubStore(),
		upload: make(map[string]*hubUpload),
	}
}

func TestHubV2RootPublic(t *testing.T) {
	h := testHub(t)
	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestHubPushRequiresAuth(t *testing.T) {
	h := testHub(t)
	body := []byte(`{"schemaVersion":2}`)
	req := httptest.NewRequest(http.MethodPut, "/v2/tunneltug/barge/manifests/latest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHubPushAndPull(t *testing.T) {
	h := testHub(t)
	payload := []byte("layer-bytes-for-barge-image")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	// Start upload + monolithic complete
	req := httptest.NewRequest(http.MethodPost, "/v2/tunneltug/barge/blobs/uploads/?digest="+digest, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("blob upload status %d: %s", rec.Code, rec.Body.String())
	}

	// Public pull blob
	req = httptest.NewRequest(http.MethodGet, "/v2/tunneltug/barge/blobs/"+digest, nil)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("blob pull %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatal("blob mismatch")
	}

	// Push manifest with auth
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"digest":"` + digest + `"}}`)
	req = httptest.NewRequest(http.MethodPut, "/v2/tunneltug/barge/manifests/latest", bytes.NewReader(manifest))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("manifest put %d: %s", rec.Code, rec.Body.String())
	}

	// Public pull manifest
	req = httptest.NewRequest(http.MethodGet, "/v2/tunneltug/barge/manifests/latest", nil)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest get %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), manifest) {
		t.Fatal("manifest mismatch")
	}

	// Tags list public
	req = httptest.NewRequest(http.MethodGet, "/v2/tunneltug/barge/tags/list", nil)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tags %d", rec.Code)
	}
	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&tags); err != nil {
		t.Fatal(err)
	}
	if len(tags.Tags) != 1 || tags.Tags[0] != "latest" {
		t.Fatalf("tags: %+v", tags.Tags)
	}
}

func TestHubBasicAuthPush(t *testing.T) {
	h := testHub(t)
	payload := []byte("x")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	req := httptest.NewRequest(http.MethodPost, "/v2/tunneltug/barge/blobs/uploads/?digest="+digest, bytes.NewReader(payload))
	req.SetBasicAuth("tunneltug", h.cfg.Token)
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("basic auth push %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHubRejectsWrongToken(t *testing.T) {
	h := testHub(t)
	req := httptest.NewRequest(http.MethodPut, "/v2/tunneltug/barge/manifests/latest", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong-token-value-that-is-long-enough")
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestValidDigestAndRepo(t *testing.T) {
	if !validRepoName("tunneltug/barge") {
		t.Fatal("expected valid repo")
	}
	if validRepoName("../evil") {
		t.Fatal("path traversal should fail")
	}
	sum := sha256.Sum256([]byte("a"))
	d := "sha256:" + hex.EncodeToString(sum[:])
	if !validDigest(d) {
		t.Fatal("digest should be valid")
	}
	if validDigest("sha256:abc") {
		t.Fatal("short digest invalid")
	}
}

func TestGenerateSecureToken_Hub(t *testing.T) {
	a, err := GenerateSecureToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 {
		t.Fatalf("len %d", len(a))
	}
	if isWeakToken(a) {
		t.Fatal("generated should not be weak")
	}
}

func TestHubChunkedUpload(t *testing.T) {
	h := testHub(t)
	part1 := []byte("hello-")
	part2 := []byte("world-barge")
	full := append(append([]byte{}, part1...), part2...)
	sum := sha256.Sum256(full)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	// Start
	req := httptest.NewRequest(http.MethodPost, "/v2/tunneltug/barge/blobs/uploads/", nil)
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start upload %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc == "" {
		t.Fatal("missing location")
	}
	uuid := rec.Header().Get("Docker-Upload-UUID")

	// Patch chunk 1
	req = httptest.NewRequest(http.MethodPatch, loc, bytes.NewReader(part1))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("patch1 %d body=%s", rec.Code, rec.Body.String())
	}

	// Complete with final chunk
	req = httptest.NewRequest(http.MethodPut, loc+"?digest="+digest, bytes.NewReader(part2))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete %d uuid=%s body=%s", rec.Code, uuid, rec.Body.String())
	}

	// Pull
	req = httptest.NewRequest(http.MethodGet, "/v2/tunneltug/barge/blobs/"+digest, nil)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	got, _ := io.ReadAll(rec.Body)
	if !bytes.Equal(got, full) {
		t.Fatalf("got %q want %q", got, full)
	}
}
