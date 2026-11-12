package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// errHubMiss is a local or upstream miss. errHubListCap stops a directory walk
// once ListPrefix has enough keys.
var (
	errHubMiss    = errors.New("not found")
	errHubListCap = errors.New("hub list cap")
)

// hubRegistry is one OCI registry served by this process.
// The request Host header selects it. Unmatched hosts use the default registry.
type hubRegistry struct {
	Name          string
	Host          string
	Public        string
	Token         string
	PullAuth      bool
	Storage       string
	Dir           string
	Upstream      string
	UpstreamToken string
	Store         hubBlobStore
	InjectCatalog bool
}

func (reg *hubRegistry) authService() string {
	if reg != nil && reg.Host != "" {
		return reg.Host
	}
	return "tunneltug-hub"
}

func (reg *hubRegistry) publicStatus() map[string]string {
	if reg == nil {
		return map[string]string{}
	}
	pull := "public"
	if reg.PullAuth {
		pull = "auth"
	}
	return map[string]string{
		"name":     reg.Name,
		"host":     reg.Host,
		"public":   reg.Public,
		"storage":  reg.Storage,
		"dir":      reg.Dir,
		"pull":     pull,
		"upstream": reg.Upstream,
	}
}

// hubRegistryConfig is one entry in -hub-registries.
type hubRegistryConfig struct {
	Name             string `yaml:"name" json:"name"`
	Host             string `yaml:"host" json:"host"`
	Public           string `yaml:"public" json:"public"`
	Storage          string `yaml:"storage" json:"storage"`
	Dir              string `yaml:"dir" json:"dir"`
	S3URL            string `yaml:"s3_url" json:"s3_url"`
	Bucket           string `yaml:"bucket" json:"bucket"`
	Prefix           string `yaml:"prefix" json:"prefix"`
	Token            string `yaml:"token" json:"token"`
	TokenEnv         string `yaml:"token_env" json:"token_env"`
	Pull             string `yaml:"pull" json:"pull"`
	Upstream         string `yaml:"upstream" json:"upstream"`
	UpstreamToken    string `yaml:"upstream_token" json:"upstream_token"`
	UpstreamTokenEnv string `yaml:"upstream_token_env" json:"upstream_token_env"`
	// resolved after load
	token         string
	upstream      string
	upstreamToken string
	pullAuth      bool
	storage       string
}

type hubRegistriesFile struct {
	Registries []hubRegistryConfig `yaml:"registries" json:"registries"`
}

var hubRegistryNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,62}[a-z0-9])?$`)

func (h *hubServer) registryFor(r *http.Request) *hubRegistry {
	host := ""
	if r != nil {
		host = canonicalHost(r.Host)
	}
	if h.byHost != nil {
		if reg := h.byHost[host]; reg != nil {
			return reg
		}
	}
	if h.def != nil {
		return h.def
	}
	return h.fallbackRegistry()
}

func (h *hubServer) fallbackRegistry() *hubRegistry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.def != nil {
		return h.def
	}
	storage := "s3"
	if h.cfg.Memory {
		storage = "memory"
	} else if strings.TrimSpace(h.cfg.Dir) != "" {
		storage = "dir"
	}
	h.def = &hubRegistry{
		Name:          "default",
		Host:          hostFromURL(h.cfg.Public),
		Public:        h.cfg.Public,
		Token:         h.cfg.Token,
		Storage:       storage,
		Dir:           h.cfg.Dir,
		Store:         h.store,
		InjectCatalog: true,
	}
	return h.def
}

func (h *hubServer) registryPublicList() []map[string]string {
	regs := h.registries
	if len(regs) == 0 {
		reg := h.def
		if reg == nil {
			reg = h.fallbackRegistry()
		}
		regs = []*hubRegistry{reg}
	}
	out := make([]map[string]string, 0, len(regs))
	for _, reg := range regs {
		out = append(out, reg.publicStatus())
	}
	return out
}

func buildHubRegistries(cfg hubConfig) (def *hubRegistry, list []*hubRegistry, byHost map[string]*hubRegistry, err error) {
	cfgs, err := assembleRegistryConfigs(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	list = make([]*hubRegistry, 0, len(cfgs))
	for _, c := range cfgs {
		reg, err := openHubRegistry(c, cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		list = append(list, reg)
	}
	byHost = make(map[string]*hubRegistry, len(list))
	for _, reg := range list {
		if reg.Host == "" {
			continue
		}
		if prev := byHost[reg.Host]; prev != nil && prev.Name != reg.Name {
			return nil, nil, nil, fmt.Errorf("hub registry host %q is used by %q and %q", reg.Host, prev.Name, reg.Name)
		}
		byHost[reg.Host] = reg
	}
	return list[0], list, byHost, nil
}

func assembleRegistryConfigs(cfg hubConfig) ([]hubRegistryConfig, error) {
	def := hubRegistryConfig{
		Name:    "default",
		Host:    hostFromURL(cfg.Public),
		Public:  cfg.Public,
		Pull:    "public",
		storage: "s3",
	}
	switch {
	case cfg.Memory:
		def.storage = "memory"
		def.Storage = "memory"
		if strings.TrimSpace(cfg.Dir) != "" {
			log.Printf("hub: -hub-memory overrides -hub-dir for the default registry")
		}
	case strings.TrimSpace(cfg.Dir) != "":
		def.storage = "dir"
		def.Storage = "dir"
		def.Dir = cfg.Dir
	default:
		def.storage = "s3"
		def.Storage = "s3"
		def.S3URL = cfg.S3URL
		def.Bucket = cfg.Bucket
	}
	def.token = cfg.Token

	path := strings.TrimSpace(cfg.Registries)
	if path == "" {
		return []hubRegistryConfig{def}, nil
	}
	fileRegs, err := loadHubRegistryFile(path)
	if err != nil {
		return nil, err
	}
	out := []hubRegistryConfig{def}
	seen := map[string]struct{}{"default": {}}
	replacedDefault := false
	for _, raw := range fileRegs {
		c, err := normalizeRegistryConfig(raw, filepath.Dir(path), cfg)
		if err != nil {
			return nil, err
		}
		if c.Name == "default" {
			if replacedDefault {
				return nil, fmt.Errorf("hub registry %q is listed more than once", c.Name)
			}
			replacedDefault = true
			// The file entry replaces the flag-built default and stays the catch-all.
			c.Host = firstNonEmpty(c.Host, def.Host)
			c.Public = firstNonEmpty(c.Public, def.Public)
			if c.token == "" {
				c.token = cfg.Token
			}
			out[0] = c
			continue
		}
		if _, ok := seen[c.Name]; ok {
			return nil, fmt.Errorf("hub registry %q is listed more than once", c.Name)
		}
		seen[c.Name] = struct{}{}
		if c.Host == "" {
			return nil, fmt.Errorf("hub registry %q needs a host", c.Name)
		}
		out = append(out, c)
	}
	hosts := map[string]string{}
	for _, c := range out {
		if c.Host == "" {
			continue
		}
		if prev, ok := hosts[c.Host]; ok && prev != c.Name {
			return nil, fmt.Errorf("hub registry host %q is used by %q and %q", c.Host, prev, c.Name)
		}
		hosts[c.Host] = c.Name
	}
	return out, nil
}

func loadHubRegistryFile(path string) ([]hubRegistryConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hub registries %s: %w", path, err)
	}
	var doc hubRegistriesFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("hub registries %s: %w", path, err)
	}
	if len(doc.Registries) == 0 {
		return nil, fmt.Errorf("hub registries %s: registries is empty", path)
	}
	return doc.Registries, nil
}

func normalizeRegistryConfig(c hubRegistryConfig, baseDir string, cfg hubConfig) (hubRegistryConfig, error) {
	c.Name = strings.ToLower(strings.TrimSpace(c.Name))
	if !hubRegistryNameRE.MatchString(c.Name) {
		return c, fmt.Errorf("hub registry name %q is invalid", c.Name)
	}
	c.Host = canonicalHost(c.Host)
	c.Public = strings.TrimRight(strings.TrimSpace(c.Public), "/")
	if c.Public == "" && c.Host != "" {
		c.Public = "https://" + c.Host
	}
	kind, err := normalizeStorageKind(c.Storage)
	if err != nil {
		return c, fmt.Errorf("hub registry %s: %w", c.Name, err)
	}
	if kind == "" {
		if c.Name == "default" {
			kind = "" // inherit flag storage later
		} else {
			return c, fmt.Errorf("hub registry %s: storage is required (dir, s3, or memory)", c.Name)
		}
	}
	c.storage = kind
	c.Dir = strings.TrimSpace(c.Dir)
	if c.Dir != "" && !filepath.IsAbs(c.Dir) {
		c.Dir = filepath.Join(baseDir, c.Dir)
	}
	c.S3URL = strings.TrimRight(strings.TrimSpace(c.S3URL), "/")
	c.Bucket = strings.Trim(strings.TrimSpace(c.Bucket), "/")
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	if err := validateStorePrefix(c.Prefix); err != nil {
		return c, fmt.Errorf("hub registry %s: %w", c.Name, err)
	}
	switch c.storage {
	case "dir":
		if c.Dir == "" {
			return c, fmt.Errorf("hub registry %s: dir is required", c.Name)
		}
	case "s3":
		if c.S3URL == "" {
			c.S3URL = cfg.S3URL
		}
		if c.Bucket == "" {
			return c, fmt.Errorf("hub registry %s: bucket is required", c.Name)
		}
	case "memory":
	case "":
		// default registry inherits flags in openHubRegistry
	default:
		return c, fmt.Errorf("hub registry %s: storage %q is unknown", c.Name, c.Storage)
	}
	tok, err := resolveRegistrySecret(c.Token, c.TokenEnv)
	if err != nil {
		return c, fmt.Errorf("hub registry %s: %w", c.Name, err)
	}
	if tok != "" && isWeakToken(tok) {
		return c, fmt.Errorf("hub registry %s: token is too weak", c.Name)
	}
	c.token = tok
	upTok, err := resolveRegistrySecret(c.UpstreamToken, c.UpstreamTokenEnv)
	if err != nil {
		return c, fmt.Errorf("hub registry %s: %w", c.Name, err)
	}
	if upTok != "" && isWeakToken(upTok) {
		return c, fmt.Errorf("hub registry %s: upstream token is too weak", c.Name)
	}
	c.upstreamToken = upTok
	c.upstream, err = validateUpstream(c.Upstream)
	if err != nil {
		return c, fmt.Errorf("hub registry %s: %w", c.Name, err)
	}
	pull := strings.ToLower(strings.TrimSpace(c.Pull))
	switch pull {
	case "", "public":
		c.pullAuth = false
	case "auth", "authenticated":
		c.pullAuth = true
	default:
		return c, fmt.Errorf("hub registry %s: pull %q must be public or auth", c.Name, c.Pull)
	}
	return c, nil
}

func openHubRegistry(c hubRegistryConfig, cfg hubConfig) (*hubRegistry, error) {
	kind := c.storage
	if kind == "" {
		switch {
		case cfg.Memory:
			kind = "memory"
		case strings.TrimSpace(cfg.Dir) != "":
			kind = "dir"
			c.Dir = cfg.Dir
		default:
			kind = "s3"
			c.S3URL = cfg.S3URL
			c.Bucket = cfg.Bucket
		}
	}
	token := c.token
	if token == "" {
		token = cfg.Token
	}
	var store hubBlobStore
	var err error
	switch kind {
	case "memory":
		store = newMemoryHubStore()
	case "dir":
		store, err = newDirHubStore(c.Dir)
	case "s3":
		if strings.TrimSpace(c.Bucket) == "" {
			return nil, fmt.Errorf("hub registry %s: s3 bucket is empty", c.Name)
		}
		origin := c.S3URL
		if origin == "" {
			origin = cfg.S3URL
		}
		store = newS3HubStore(origin, c.Bucket, token)
	default:
		return nil, fmt.Errorf("hub registry %s: storage %q is unknown", c.Name, kind)
	}
	if err != nil {
		return nil, fmt.Errorf("hub registry %s: %w", c.Name, err)
	}
	store = wrapPrefix(store, c.Prefix)
	return &hubRegistry{
		Name:          c.Name,
		Host:          c.Host,
		Public:        firstNonEmpty(c.Public, cfg.Public),
		Token:         token,
		PullAuth:      c.pullAuth,
		Storage:       kind,
		Dir:           c.Dir,
		Upstream:      c.upstream,
		UpstreamToken: c.upstreamToken,
		Store:         store,
		InjectCatalog: c.Name == "default",
	}, nil
}

func normalizeStorageKind(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", nil
	case "dir", "directory", "fs", "filesystem", "disk":
		return "dir", nil
	case "s3":
		return "s3", nil
	case "memory":
		return "memory", nil
	default:
		return "", fmt.Errorf("storage %q is unknown", raw)
	}
}

func validateStorePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if strings.Contains(prefix, "..") || strings.Contains(prefix, "\\") || strings.Contains(prefix, ":") {
		return fmt.Errorf("prefix %q is invalid", prefix)
	}
	for _, seg := range strings.Split(prefix, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("prefix %q is invalid", prefix)
		}
	}
	return nil
}

func resolveRegistrySecret(inline, envName string) (string, error) {
	envName = strings.TrimSpace(envName)
	if envName != "" {
		v := strings.TrimSpace(os.Getenv(envName))
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", envName)
		}
		return v, nil
	}
	return strings.TrimSpace(inline), nil
}

func validateUpstream(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("upstream: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("upstream scheme must be http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("upstream host is required")
	}
	if u.User != nil {
		return "", fmt.Errorf("upstream URL must not include userinfo")
	}
	return raw, nil
}

func canonicalHost(hostport string) string {
	hostport = strings.TrimSpace(strings.ToLower(hostport))
	if hostport == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(hostport, "[]")
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		return canonicalHost(raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return canonicalHost(u.Host)
}

func (h *hubServer) allowPull(w http.ResponseWriter, r *http.Request) bool {
	reg := h.registryFor(r)
	if reg == nil || !reg.PullAuth {
		return true
	}
	return h.requireToken(w, r, reg.Token, reg.authService())
}

var hubUpstreamClient = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("redirect scheme %s", req.URL.Scheme)
		}
		return nil
	},
}

func (h *hubServer) readManifest(r *http.Request, name, ref string) ([]byte, string, error) {
	reg := h.registryFor(r)
	key := hubManifestKey(name, ref)
	data, ct, err := reg.Store.Get(key)
	if err == nil {
		return data, ct, nil
	}
	if reg.Upstream == "" {
		return nil, "", err
	}
	accept := r.Header.Get("Accept")
	if accept == "" {
		accept = "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json"
	}
	data, ct, err = h.upstreamGet(reg, ociNamePath(name, "manifests/"+url.PathEscape(ref)), accept, 32<<20)
	if err != nil {
		return nil, "", err
	}
	sum := sha256Sum(data)
	if validDigest(ref) && !strings.EqualFold(sum, ref) {
		log.Printf("hub upstream manifest digest mismatch for %s", name)
		return nil, "", &hubFetchError{Status: http.StatusBadGateway}
	}
	if ct == "" {
		ct = "application/vnd.docker.distribution.manifest.v2+json"
	}
	if putErr := reg.Store.Put(key, data, ct); putErr != nil {
		log.Printf("hub pull-through store manifest: %v", putErr)
	}
	_ = reg.Store.Put(hubManifestKey(name, sum), data, ct)
	if !strings.HasPrefix(strings.ToLower(ref), "sha256:") {
		_ = h.indexTag(reg.Store, name, ref)
	}
	return data, ct, nil
}

func (h *hubServer) readBlob(r *http.Request, name, digest string) ([]byte, string, error) {
	reg := h.registryFor(r)
	key := hubBlobKey(digest)
	data, ct, err := reg.Store.Get(key)
	if err == nil {
		return data, ct, nil
	}
	if reg.Upstream == "" {
		return nil, "", err
	}
	data, ct, err = h.upstreamGet(reg, ociNamePath(name, "blobs/"+url.PathEscape(digest)), "application/octet-stream", 512<<20)
	if err != nil {
		return nil, "", err
	}
	if !strings.EqualFold(sha256Sum(data), digest) {
		log.Printf("hub upstream blob digest mismatch for %s", digest)
		return nil, "", &hubFetchError{Status: http.StatusBadGateway}
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	if putErr := reg.Store.Put(key, data, ct); putErr != nil {
		log.Printf("hub pull-through store blob: %v", putErr)
	}
	return data, ct, nil
}

func (h *hubServer) upstreamGet(reg *hubRegistry, ociPath, accept string, max int64) ([]byte, string, error) {
	if reg == nil || reg.Upstream == "" {
		return nil, "", errHubMiss
	}
	req, err := http.NewRequest(http.MethodGet, reg.Upstream+ociPath, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "tunneltug-hub")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if reg.UpstreamToken != "" {
		req.Header.Set("Authorization", "Bearer "+reg.UpstreamToken)
	}
	resp, err := hubUpstreamClient.Do(req)
	if err != nil {
		log.Printf("hub upstream %s: %v", ociPath, err)
		return nil, "", &hubFetchError{Status: http.StatusBadGateway}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", errHubMiss
	}
	if resp.StatusCode >= 300 {
		log.Printf("hub upstream %s: %s", ociPath, resp.Status)
		return nil, "", &hubFetchError{Status: resp.StatusCode}
	}
	data, err := readLimited(resp.Body, max)
	if err != nil {
		log.Printf("hub upstream %s: %v", ociPath, err)
		return nil, "", &hubFetchError{Status: http.StatusBadGateway}
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func (h *hubServer) upstreamTags(reg *hubRegistry, name string) ([]string, error) {
	data, _, err := h.upstreamGet(reg, ociNamePath(name, "tags/list"), "application/json", 4<<20)
	if err != nil {
		return nil, err
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	return body.Tags, nil
}

type hubFetchError struct {
	Status int
}

func (e *hubFetchError) Error() string {
	if e == nil {
		return "upstream registry error"
	}
	return fmt.Sprintf("upstream registry status %d", e.Status)
}

func ociNamePath(name, rest string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/v2/" + strings.Join(parts, "/") + "/" + rest
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("object exceeds %d bytes", max)
	}
	return data, nil
}

func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func pageStrings(list []string, n int, last string) (page []string, next string) {
	if list == nil {
		list = []string{}
	}
	if last != "" {
		filtered := make([]string, 0, len(list))
		for _, name := range list {
			if name > last {
				filtered = append(filtered, name)
			}
		}
		list = filtered
	}
	if n > 0 && len(list) > n {
		next = list[n-1]
		list = append([]string(nil), list[:n]...)
	}
	if list == nil {
		list = []string{}
	}
	return list, next
}

func reposFromManifestKeys(keys []string) []string {
	repos := map[string]struct{}{}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, "manifests/")
		if rest == k || strings.HasSuffix(rest, ".type") {
			continue
		}
		parts := strings.Split(rest, "/")
		if len(parts) < 2 {
			continue
		}
		repo := strings.Join(parts[:len(parts)-1], "/")
		if validRepoName(repo) {
			repos[repo] = struct{}{}
		}
	}
	list := make([]string, 0, len(repos))
	for name := range repos {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
}

func unionStrings(a, b []string) []string {
	seen := map[string]struct{}{}
	for _, s := range a {
		if s != "" {
			seen[s] = struct{}{}
		}
	}
	for _, s := range b {
		if s != "" {
			seen[s] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
