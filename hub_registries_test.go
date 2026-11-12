package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirHubStoreRoundTripAndEscape(t *testing.T) {
	root := t.TempDir()
	st, err := newDirHubStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newDirHubStore(filepath.VolumeName(root) + string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root was accepted")
	}
	if err := st.Put("../escape", []byte("x"), "text/plain"); err == nil {
		t.Fatal("expected traversal to fail")
	}
	if err := st.Put("manifests/../../escape", []byte("x"), "text/plain"); err == nil {
		t.Fatal("expected collapsed traversal to fail")
	}
	outside := filepath.Join(filepath.Dir(root), "escape")
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("wrote outside the store root")
	}

	key := "manifests/acme/widget/sha256:" + strings.Repeat("ab", 32)
	if err := st.Put(key, []byte("manifest"), "application/json"); err != nil {
		t.Fatal(err)
	}
	got, ct, err := st.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "manifest" || ct != "application/json" {
		t.Fatalf("got %q %q", got, ct)
	}
	keys, err := st.ListPrefix("manifests/")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range keys {
		if k == key {
			found = true
		}
		if strings.Contains(k, "~") {
			t.Fatalf("listed encoded key %q", k)
		}
	}
	if !found {
		t.Fatalf("keys %v", keys)
	}
}

func TestHubDirDefaultRegistry(t *testing.T) {
	dir := t.TempDir()
	token := "default-token-0123456789abcdef"
	h, err := newHubServer(hubConfig{
		Public: "https://registry.example",
		Token:  token,
		Dir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.def == nil || h.def.Storage != "dir" {
		t.Fatalf("storage %+v", h.def)
	}
	manifest := []byte(`{"schemaVersion":2}`)
	hubPutManifest(t, h, "registry.example", "tunneltug/engine", "latest", token, manifest)
	code, got := hubGet(t, h, "registry.example", "/v2/tunneltug/engine/manifests/latest", "")
	if code != http.StatusOK || !bytes.Equal(got, manifest) {
		t.Fatalf("pull %d %s", code, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifests", "tunneltug", "engine", "latest")); err != nil {
		t.Fatal(err)
	}
}

func TestHubRegistriesIsolateByHost(t *testing.T) {
	dir := t.TempDir()
	productsToken := "products-token-0123456789abcdef"
	fleetToken := "fleet-token-0123456789abcdef"
	defaultToken := "default-token-0123456789abcdef"
	t.Setenv("TUNNELTUG_FLEET_HUB_TOKEN", fleetToken)
	yamlPath := filepath.Join(dir, "registries.yaml")
	body := []byte(`
registries:
  - name: products
    host: products.example
    public: https://products.example
    storage: dir
    dir: products
    pull: public
    token: ` + productsToken + `
  - name: fleet
    host: fleet.example
    storage: dir
    dir: fleet
    pull: auth
    token_env: TUNNELTUG_FLEET_HUB_TOKEN
`)
	if err := os.WriteFile(yamlPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := newHubServer(hubConfig{
		Public:     "https://hub.tunneltug.com",
		Token:      defaultToken,
		Memory:     true,
		Registries: yamlPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`)
	hubPutManifest(t, h, "products.example", "library/alpine", "latest", productsToken, manifest)
	hubPutManifest(t, h, "products.example", "library/busybox", "1.36", productsToken, manifest)

	// Fleet does not see the products manifest, and its pull requires its own token.
	if code, _ := hubGet(t, h, "fleet.example", "/v2/library/alpine/manifests/latest", ""); code != http.StatusUnauthorized {
		t.Fatalf("fleet anonymous pull %d", code)
	}
	if code, _ := hubGet(t, h, "fleet.example", "/v2/library/alpine/manifests/latest", productsToken); code != http.StatusUnauthorized {
		t.Fatalf("products token on fleet %d", code)
	}
	if code, _ := hubGet(t, h, "fleet.example", "/v2/library/alpine/manifests/latest", fleetToken); code != http.StatusNotFound {
		t.Fatalf("fleet miss %d", code)
	}

	code, got := hubGet(t, h, "products.example", "/v2/library/alpine/manifests/latest", "")
	if code != http.StatusOK || !bytes.Equal(got, manifest) {
		t.Fatalf("products pull %d %s", code, got)
	}
	// Catch-all host is the default registry, a different store.
	if code, _ := hubGet(t, h, "example.com", "/v2/library/alpine/manifests/latest", ""); code != http.StatusNotFound {
		t.Fatalf("default saw products manifest %d", code)
	}
	hubPutManifest(t, h, "hub.tunneltug.com", "tunneltug/engine", "latest", defaultToken, manifest)
	code, got = hubGet(t, h, "example.com", "/v2/tunneltug/engine/manifests/latest", "")
	if code != http.StatusOK || !bytes.Equal(got, manifest) {
		t.Fatalf("default catch-all %d %s", code, got)
	}

	layer := []byte("products-layer")
	sum := sha256.Sum256(layer)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	req := httptest.NewRequest(http.MethodPost, "/v2/library/alpine/blobs/uploads/?digest="+digest, bytes.NewReader(layer))
	req.Host = "products.example"
	req.Header.Set("Authorization", "Bearer "+productsToken)
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("blob put %d %s", rec.Code, rec.Body.String())
	}
	blobPath := filepath.Join(dir, "products", "blobs", "sha256", hex.EncodeToString(sum[:]))
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("blob file: %v", err)
	}
	if code, _ := hubGet(t, h, "fleet.example", "/v2/library/alpine/blobs/"+digest, fleetToken); code != http.StatusNotFound {
		t.Fatalf("fleet saw products blob %d", code)
	}

	// Mount of an existing blob in the same registry.
	req = httptest.NewRequest(http.MethodPost, "/v2/library/other/blobs/uploads/?mount="+digest+"&from=library/alpine", nil)
	req.Host = "products.example"
	req.Header.Set("Authorization", "Bearer "+productsToken)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mount %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/_catalog?n=1", nil)
	req.Host = "products.example"
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog %d", rec.Code)
	}
	var page struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Repositories) != 1 || page.Repositories[0] != "library/alpine" {
		t.Fatalf("page %+v", page.Repositories)
	}
	if !strings.Contains(rec.Header().Get("Link"), `rel="next"`) {
		t.Fatalf("link %q", rec.Header().Get("Link"))
	}

	req = httptest.NewRequest(http.MethodGet, "/_tunneltug/hub/health", nil)
	rec = httptest.NewRecorder()
	h.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health %d", rec.Code)
	}
	health := rec.Body.String()
	for _, want := range []string{`"name":"products"`, `"name":"fleet"`, `"name":"default"`, `"pull":"auth"`} {
		if !strings.Contains(health, want) {
			t.Fatalf("health missing %s: %s", want, health)
		}
	}
	if strings.Contains(health, productsToken) || strings.Contains(health, fleetToken) {
		t.Fatal("health leaked a token")
	}
}

func TestHubPullThroughCaches(t *testing.T) {
	upstream := testHub(t)
	layer := []byte("mirrored-layer")
	sum := sha256.Sum256(layer)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	manifest := []byte(`{"schemaVersion":2,"layers":[{"digest":"` + digest + `"}]}`)

	var sawAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		upstream.handler().ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)

	req := httptest.NewRequest(http.MethodPost, "/v2/library/alpine/blobs/uploads/?digest="+digest, bytes.NewReader(layer))
	req.Header.Set("Authorization", "Bearer "+upstream.cfg.Token)
	rec := httptest.NewRecorder()
	upstream.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upstream blob %d %s", rec.Code, rec.Body.String())
	}
	hubPutManifest(t, upstream, "example.com", "library/alpine", "latest", upstream.cfg.Token, manifest)

	dir := t.TempDir()
	upstreamToken := "upstream-token-0123456789abcdef"
	clientToken := "client-token-0123456789abcdef"
	yamlPath := filepath.Join(dir, "registries.yaml")
	raw := []byte("registries:\n  - name: mirror\n    host: mirror.example\n    storage: dir\n    dir: mirror\n    pull: public\n    upstream: " + up.URL + "\n    upstream_token: " + upstreamToken + "\n")
	if err := os.WriteFile(yamlPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := newHubServer(hubConfig{
		Public:     "https://hub.tunneltug.com",
		Token:      "default-token-0123456789abcdef",
		Memory:     true,
		Registries: yamlPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/library/alpine/manifests/latest", nil)
	req.Host = "mirror.example"
	req.Header.Set("Authorization", "Bearer "+clientToken)
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), manifest) {
		t.Fatalf("mirror manifest %d %s", rec.Code, rec.Body.String())
	}
	if sawAuth != "Bearer "+upstreamToken {
		t.Fatalf("upstream auth %q", sawAuth)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/library/alpine/blobs/"+digest, nil)
	req.Host = "mirror.example"
	rec = httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), layer) {
		t.Fatalf("mirror blob %d %s", rec.Code, rec.Body.String())
	}

	up.Close()
	code, got := hubGet(t, h, "mirror.example", "/v2/library/alpine/manifests/latest", "")
	if code != http.StatusOK || !bytes.Equal(got, manifest) {
		t.Fatalf("cached manifest %d %s", code, got)
	}
	code, got = hubGet(t, h, "mirror.example", "/v2/library/alpine/blobs/"+digest, "")
	if code != http.StatusOK || !bytes.Equal(got, layer) {
		t.Fatalf("cached blob %d %s", code, got)
	}
}

func TestHubPullThroughRejectsBadDigest(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-the-blob"))
	}))
	t.Cleanup(bad.Close)
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "registries.yaml")
	raw := []byte("registries:\n  - name: mirror\n    host: mirror.example\n    storage: memory\n    upstream: " + bad.URL + "\n")
	if err := os.WriteFile(yamlPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := newHubServer(hubConfig{
		Public:     "https://hub.tunneltug.com",
		Token:      "default-token-0123456789abcdef",
		Memory:     true,
		Registries: yamlPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("expected"))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	code, _ := hubGet(t, h, "mirror.example", "/v2/library/alpine/blobs/"+digest, "")
	if code != http.StatusBadGateway {
		t.Fatalf("status %d", code)
	}
}

func TestHubRegistriesRejectsBadFile(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "scheme",
			body: "registries:\n  - name: mirror\n    host: mirror.example\n    storage: memory\n    upstream: file:///tmp/x\n",
			want: "http or https",
		},
		{
			name: "userinfo",
			body: "registries:\n  - name: mirror\n    host: mirror.example\n    storage: memory\n    upstream: https://user:pass@example.com\n",
			want: "userinfo",
		},
		{
			name: "duplicate-host",
			body: "registries:\n  - name: a\n    host: same.example\n    storage: memory\n  - name: b\n    host: same.example\n    storage: memory\n",
			want: "used by",
		},
		{
			name: "weak",
			body: "registries:\n  - name: fleet\n    host: fleet.example\n    storage: memory\n    token: secret123\n",
			want: "too weak",
		},
		{
			name: "nodir",
			body: "registries:\n  - name: fleet\n    host: fleet.example\n    storage: dir\n",
			want: "dir is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := newHubServer(hubConfig{
				Public:     "https://hub.tunneltug.com",
				Token:      "default-token-0123456789abcdef",
				Memory:     true,
				Registries: path,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func hubPutManifest(t *testing.T, h *hubServer, host, repo, tag, token string, body []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v2/"+repo+"/manifests/"+tag, bytes.NewReader(body))
	req.Host = host
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("put %s %s:%s: %d %s", host, repo, tag, rec.Code, rec.Body.String())
	}
}

func hubGet(t *testing.T, h *hubServer, host, path, token string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.handleV2(rec, req)
	return rec.Code, rec.Body.Bytes()
}
