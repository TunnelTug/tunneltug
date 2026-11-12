package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SBOM support for hub.tunneltug.com product barges.
//
// On hub-publish, TunnelTug generates a CycloneDX 1.5 JSON SBOM from the
// product binary (Go buildinfo modules) and stores it next to the image:
//   store key: sboms/<repo>/<tag>.cdx.json
//
// Public review:
//   GET /_tunneltug/hub/sbom/<repo>?tag=latest          → raw CycloneDX JSON
//   GET /_tunneltug/hub/sbom/<repo>/view?tag=latest     → HTML reviewer
//   GET /_tunneltug/hub/sboms                           → catalog of available SBOMs
// Authenticated publish (hub-publish client):
//   PUT /_tunneltug/hub/sbom/<repo>?tag=latest          → store body

const cycloneDXSpec = "1.5"

type cdxBOM struct {
	BOMFormat    string         `json:"bomFormat"`
	SpecVersion  string         `json:"specVersion"`
	SerialNumber string         `json:"serialNumber,omitempty"`
	Version      int            `json:"version"`
	Metadata     cdxMetadata    `json:"metadata"`
	Components   []cdxComponent `json:"components"`
}

type cdxMetadata struct {
	Timestamp string       `json:"timestamp,omitempty"`
	Tools     []cdxTool    `json:"tools,omitempty"`
	Component cdxComponent `json:"component"`
}

type cdxTool struct {
	Vendor  string `json:"vendor,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type cdxComponent struct {
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	Version    string    `json:"version,omitempty"`
	BOMRef     string    `json:"bom-ref,omitempty"`
	PURL       string    `json:"purl,omitempty"`
	Hashes     []cdxHash `json:"hashes,omitempty"`
	Properties []cdxProp `json:"properties,omitempty"`
}

type cdxHash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type cdxProp struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func hubSBOMKey(repo, tag string) string {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	tag = strings.TrimSpace(tag)
	if tag == "" {
		tag = "latest"
	}
	return "sboms/" + repo + "/" + tag + ".cdx.json"
}

// generateCycloneDXFromBinary builds a CycloneDX 1.5 SBOM for a product binary.
func generateCycloneDXFromBinary(binPath, repo, tag string) ([]byte, error) {
	raw, err := os.ReadFile(binPath)
	if err != nil {
		return nil, fmt.Errorf("read binary: %w", err)
	}
	sum := sha256.Sum256(raw)
	shaHex := hex.EncodeToString(sum[:])

	repo = strings.Trim(strings.TrimSpace(repo), "/")
	tag = strings.TrimSpace(tag)
	if tag == "" {
		tag = "latest"
	}
	name := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		name = repo[i+1:]
	}

	main := cdxComponent{
		Type:    "application",
		Name:    name,
		Version: tag,
		BOMRef:  "app:" + repo + "@" + tag,
		PURL:    "pkg:generic/" + repo + "@" + tag,
		Hashes: []cdxHash{{
			Alg:     "SHA-256",
			Content: shaHex,
		}},
		Properties: []cdxProp{
			{Name: "tunneltug:hub-repo", Value: repo},
			{Name: "tunneltug:hub-tag", Value: tag},
			{Name: "tunneltug:binary", Value: filepath.Base(binPath)},
			{Name: "tunneltug:size", Value: fmt.Sprintf("%d", len(raw))},
		},
	}

	components := []cdxComponent{}
	if bi, err := buildinfo.ReadFile(binPath); err == nil && bi != nil {
		main.Properties = append(main.Properties,
			cdxProp{Name: "go:version", Value: bi.GoVersion},
			cdxProp{Name: "go:path", Value: bi.Path},
		)
		if bi.Main.Path != "" {
			main.Properties = append(main.Properties,
				cdxProp{Name: "go:main", Value: bi.Main.Path},
				cdxProp{Name: "go:main-version", Value: bi.Main.Version},
			)
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" || s.Key == "vcs.time" || s.Key == "vcs.modified" {
				main.Properties = append(main.Properties, cdxProp{Name: "go:" + s.Key, Value: s.Value})
			}
		}
		seen := map[string]bool{}
		for _, m := range bi.Deps {
			if m.Path == "" || seen[m.Path+"@"+m.Version] {
				continue
			}
			seen[m.Path+"@"+m.Version] = true
			ver := m.Version
			if ver == "" {
				ver = "unknown"
			}
			comp := cdxComponent{
				Type:    "library",
				Name:    m.Path,
				Version: ver,
				BOMRef:  "pkg:golang/" + m.Path + "@" + ver,
				PURL:    "pkg:golang/" + m.Path + "@" + ver,
			}
			if m.Sum != "" {
				// Go module sum is h1:<base64> — record as property; not a raw SHA.
				comp.Properties = []cdxProp{{Name: "go:module-sum", Value: m.Sum}}
			}
			components = append(components, comp)
		}
		sort.Slice(components, func(i, j int) bool {
			return components[i].Name < components[j].Name
		})
	}

	toolVer := strings.TrimSpace(Version)
	if toolVer == "" {
		toolVer = "dev"
	}
	bom := cdxBOM{
		BOMFormat:    "CycloneDX",
		SpecVersion:  cycloneDXSpec,
		SerialNumber: "urn:uuid:tunneltug-" + shaHex[:16],
		Version:      1,
		Metadata: cdxMetadata{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Tools: []cdxTool{{
				Vendor:  "TunnelTug",
				Name:    "tunneltug-hub-publish",
				Version: toolVer,
			}},
			Component: main,
		},
		Components: components,
	}
	return json.MarshalIndent(bom, "", "  ")
}

func (h *hubServer) handleSBOMCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	keys, err := h.store.ListPrefix("sboms/")
	if err != nil {
		http.Error(w, "list failed", http.StatusBadGateway)
		return
	}
	type row struct {
		Repo string `json:"repo"`
		Tag  string `json:"tag"`
		URL  string `json:"url"`
		View string `json:"view"`
	}
	var out []row
	for _, k := range keys {
		// sboms/<repo...>/<tag>.cdx.json
		if !strings.HasPrefix(k, "sboms/") || !strings.HasSuffix(k, ".cdx.json") {
			continue
		}
		rest := strings.TrimPrefix(k, "sboms/")
		rest = strings.TrimSuffix(rest, ".cdx.json")
		slash := strings.LastIndex(rest, "/")
		if slash < 0 {
			continue
		}
		repo, tag := rest[:slash], rest[slash+1:]
		base := strings.TrimRight(h.cfg.Public, "/")
		out = append(out, row{
			Repo: repo,
			Tag:  tag,
			URL:  base + "/_tunneltug/hub/sbom/" + repo + "?tag=" + tag,
			View: base + "/_tunneltug/hub/sbom/" + repo + "/view?tag=" + tag,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo == out[j].Repo {
			return out[i].Tag < out[j].Tag
		}
		return out[i].Repo < out[j].Repo
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"count": len(out),
		"sboms": out,
	})
}

func (h *hubServer) handleSBOM(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/_tunneltug/hub/sbom/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "repository required", http.StatusBadRequest)
		return
	}
	view := false
	if strings.HasSuffix(path, "/view") {
		view = true
		path = strings.TrimSuffix(path, "/view")
		path = strings.Trim(path, "/")
	}
	if path == "" || !validRepoName(path) {
		http.Error(w, "invalid repository", http.StatusBadRequest)
		return
	}
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag == "" {
		tag = "latest"
	}
	key := hubSBOMKey(path, tag)

	switch r.Method {
	case http.MethodPut, http.MethodPost:
		if !h.authorizeHubWrite(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil || len(body) == 0 {
			http.Error(w, "empty body", http.StatusBadRequest)
			return
		}
		// Light validation: must look like CycloneDX JSON.
		var probe map[string]interface{}
		if json.Unmarshal(body, &probe) != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if fmt.Sprint(probe["bomFormat"]) != "CycloneDX" && fmt.Sprint(probe["bomFormat"]) != "cyclonedx" {
			// Still accept if it has components array (lenient for tools).
			if _, ok := probe["components"]; !ok {
				http.Error(w, "not a CycloneDX document", http.StatusBadRequest)
				return
			}
		}
		if err := h.store.Put(key, body, "application/vnd.cyclonedx+json"); err != nil {
			http.Error(w, "store failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "stored",
			"repo":   path,
			"tag":    tag,
			"key":    key,
			"view":   strings.TrimRight(h.cfg.Public, "/") + "/_tunneltug/hub/sbom/" + path + "/view?tag=" + tag,
		})
		return

	case http.MethodGet, http.MethodHead:
		data, ct, err := h.store.Get(key)
		if err != nil || len(data) == 0 {
			http.Error(w, "sbom not found for "+path+":"+tag, http.StatusNotFound)
			return
		}
		if view {
			h.writeSBOMView(w, path, tag, data)
			return
		}
		if ct == "" {
			ct = "application/vnd.cyclonedx+json"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(data)
		return

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *hubServer) authorizeHubWrite(r *http.Request) bool {
	if h == nil || strings.TrimSpace(h.cfg.Token) == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		tok := strings.TrimSpace(auth[7:])
		return tok != "" && tok == h.cfg.Token
	}
	// Also accept X-TunnelTug-Token used elsewhere.
	if t := strings.TrimSpace(r.Header.Get("X-TunnelTug-Token")); t != "" && t == h.cfg.Token {
		return true
	}
	return false
}

func (h *hubServer) writeSBOMView(w http.ResponseWriter, repo, tag string, raw []byte) {
	var bom cdxBOM
	_ = json.Unmarshal(raw, &bom)
	title := htmlEsc(repo + ":" + tag + " — SBOM")
	mainName := bom.Metadata.Component.Name
	if mainName == "" {
		mainName = repo
	}
	mainVer := bom.Metadata.Component.Version
	if mainVer == "" {
		mainVer = tag
	}
	var hash string
	for _, hsh := range bom.Metadata.Component.Hashes {
		if strings.EqualFold(hsh.Alg, "SHA-256") {
			hash = hsh.Content
			break
		}
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>` + title + `</title>
<style>
:root{--bg:#0a0e17;--panel:#111827;--border:#1f2937;--text:#f3f4f6;--muted:#9ca3af;--accent:#22d3ee;--ok:#34d399}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font-family:ui-sans-serif,system-ui,sans-serif;line-height:1.5}
.wrap{max-width:960px;margin:0 auto;padding:32px 18px 64px}
h1{font-size:1.4rem;margin:0 0 6px}h2{font-size:1rem;color:var(--accent);text-transform:uppercase;letter-spacing:.04em;margin:24px 0 10px}
.lead{color:var(--muted);margin:0 0 18px;font-size:14px}
.card{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:16px 18px;margin:0 0 14px}
.meta{font-size:13px;color:var(--muted)}code{font-size:12px;color:#e6edf3;word-break:break-all}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:8px 6px;border-bottom:1px solid var(--border);vertical-align:top}
th{color:var(--muted);font-weight:600;font-size:11px;text-transform:uppercase;letter-spacing:.04em}
a{color:var(--accent)} .badge{display:inline-block;padding:2px 8px;border-radius:999px;background:rgba(34,211,238,.12);color:var(--accent);font-size:11px;font-weight:600}
.actions{display:flex;gap:10px;flex-wrap:wrap;margin:12px 0 0}
.btn{display:inline-block;padding:8px 12px;border-radius:8px;border:1px solid var(--border);background:#1e293b;color:var(--text);text-decoration:none;font-size:13px;font-weight:600}
.btn:hover{border-color:var(--accent);color:var(--accent)}
</style></head><body><div class="wrap">
<p><a href="/">← hub.tunneltug.com</a></p>
<span class="badge">CycloneDX ` + htmlEsc(bom.SpecVersion) + `</span>
<h1>` + htmlEsc(mainName) + ` <span style="color:var(--muted);font-weight:500">` + htmlEsc(mainVer) + `</span></h1>
<p class="lead">Software Bill of Materials for barge image <code>` + htmlEsc(repo) + `:` + htmlEsc(tag) + `</code>.</p>
<div class="card">
  <div class="meta">Components: <strong style="color:var(--ok)">` + fmt.Sprintf("%d", len(bom.Components)) + `</strong></div>`)
	if hash != "" {
		b.WriteString(`<div class="meta" style="margin-top:6px">SHA-256: <code>` + htmlEsc(hash) + `</code></div>`)
	}
	if bom.Metadata.Timestamp != "" {
		b.WriteString(`<div class="meta" style="margin-top:6px">Generated: ` + htmlEsc(bom.Metadata.Timestamp) + `</div>`)
	}
	rawURL := "/_tunneltug/hub/sbom/" + repo + "?tag=" + tag
	b.WriteString(`<div class="actions"><a class="btn" href="` + htmlEsc(rawURL) + `">Download JSON</a>
<a class="btn" href="/">Back to catalog</a></div></div>
<h2>Dependencies</h2><div class="card" style="padding:0;overflow:auto"><table>
<thead><tr><th>Name</th><th>Version</th><th>PURL</th></tr></thead><tbody>`)
	if len(bom.Components) == 0 {
		b.WriteString(`<tr><td colspan="3" class="meta" style="padding:14px">No library components recorded (binary may lack Go buildinfo).</td></tr>`)
	}
	for _, c := range bom.Components {
		b.WriteString("<tr><td><code>" + htmlEsc(c.Name) + "</code></td><td>" + htmlEsc(c.Version) + "</td><td class=\"meta\"><code>" + htmlEsc(c.PURL) + "</code></td></tr>")
	}
	b.WriteString(`</tbody></table></div>
<h2>Raw document</h2><div class="card"><pre style="margin:0;white-space:pre-wrap;font-size:11px;max-height:320px;overflow:auto">` + htmlEsc(string(raw)) + `</pre></div>
</div></body></html>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// publishSBOMToHub PUTs a CycloneDX document for repo:tag (authenticated).
func publishSBOMToHub(baseURL, repo, tag, token string, bom []byte) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	repo = strings.Trim(repo, "/")
	if tag == "" {
		tag = "latest"
	}
	url := fmt.Sprintf("%s/_tunneltug/hub/sbom/%s?tag=%s", baseURL, repo, tag)
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(bom)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/vnd.cyclonedx+json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sbom publish %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// generateAndPublishSBOM creates an SBOM from binPath and uploads it to the hub.
func generateAndPublishSBOM(binPath, host, repo, tag, token string) error {
	bom, err := generateCycloneDXFromBinary(binPath, repo, tag)
	if err != nil {
		return err
	}
	return publishSBOMToHub(hubBaseURL(host), repo, tag, token, bom)
}
