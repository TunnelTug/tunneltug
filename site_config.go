package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Site configuration — declarative multi-PoP global ingress + kernel replication mesh.
//
// Load with:
//
//	tunneltug -config config/site.example.yaml -pop sfo ...
//	tunneltug -config site.tug -pop sfo ...   # Junos-like Tugconf (set/delete)
//
// Same IR for YAML and Tugconf. CLI flags always win over site file values.

const siteAPIVersion = "tunneltug/v1"
const siteKind = "Site"

// SiteConfig is the root document for complex multi-region deployments.
type SiteConfig struct {
	APIVersion string             `yaml:"apiVersion" json:"apiVersion"`
	Kind       string             `yaml:"kind" json:"kind"`
	Site       SiteMeta           `yaml:"site" json:"site"`
	Mesh       *SiteMeshBlock     `yaml:"mesh" json:"mesh"`
	Hub        *SiteHubBlock      `yaml:"hub" json:"hub"`
	Stack      *StackConfigFile   `yaml:"stack" json:"stack"`
	KernelMesh *KernelMeshPolicy  `yaml:"kernel_mesh" json:"kernel_mesh"`
	Pops       []PopConfig        `yaml:"pops" json:"pops"`
	Process    *ProcessProfile    `yaml:"process" json:"process"`
}

// SiteMeta is site-wide identity (no hardcoded product domains).
type SiteMeta struct {
	Name         string `yaml:"name" json:"name"`
	Domain       string `yaml:"domain" json:"domain"`
	PublicScheme string `yaml:"public_scheme" json:"public_scheme"`
	// TokenEnv is the env var holding the crypto token (never put secrets in YAML).
	TokenEnv string `yaml:"token_env" json:"token_env"`
}

// SiteMeshBlock maps to -mesh-* flags.
type SiteMeshBlock struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	TLD     string `yaml:"tld" json:"tld"`
	Zone    string `yaml:"zone" json:"zone"`
	NS      string `yaml:"ns" json:"ns"`
	EdgeIP  string `yaml:"edge_ip" json:"edge_ip"`
	DataDir string `yaml:"data_dir" json:"data_dir"`
	DNS     string `yaml:"dns" json:"dns"` // mesh DNS listen
}

// SiteHubBlock maps to hub registry settings.
type SiteHubBlock struct {
	Host string `yaml:"host" json:"host"`
	Tag  string `yaml:"tag" json:"tag"`
}

// KernelMeshPolicy controls how ultimate_db peers are expanded across pops.
// Products keep local embeds; these peers are for real-time replication only.
type KernelMeshPolicy struct {
	Mode      string `yaml:"mode" json:"mode"`           // full-mesh | hub-spoke | manual
	HubPop    string `yaml:"hub_pop" json:"hub_pop"`     // when hub-spoke
	Transport string `yaml:"transport" json:"transport"` // http | https (informational; URLs are explicit)
}

// PopConfig is one global ingress / region point of presence.
type PopConfig struct {
	ID             string         `yaml:"id" json:"id"`
	Region         string         `yaml:"region" json:"region"`
	Roles          []string       `yaml:"roles" json:"roles"`
	Domain         string         `yaml:"domain" json:"domain"`
	Tunnel         *TunnelBlock   `yaml:"tunnel" json:"tunnel"`
	Anycast        *FileOrInline  `yaml:"anycast" json:"anycast"`
	Kernel         *PopKernel     `yaml:"kernel" json:"kernel"`
	VHosts         *FileOrInline  `yaml:"vhosts" json:"vhosts"`
	DNS            *FileOrInline  `yaml:"dns" json:"dns"`
	StackOverrides *StackConfigFile `yaml:"stack_overrides" json:"stack_overrides"`
}

// TunnelBlock is per-PoP tunnel/LB/barge engine settings.
type TunnelBlock struct {
	Public  string      `yaml:"public" json:"public"`
	Control string      `yaml:"control" json:"control"`
	Prod    bool        `yaml:"prod" json:"prod"`
	Dev     bool        `yaml:"dev" json:"dev"`
	Email   string      `yaml:"email" json:"email"`
	Routing string      `yaml:"routing" json:"routing"`
	LB      *PopLBBlock `yaml:"lb" json:"lb"`
	Barge   *PopBargeBlock `yaml:"barge" json:"barge"`
}

// PopLBBlock is load-balancer settings for a PoP.
type PopLBBlock struct {
	Backends string `yaml:"backends" json:"backends"`
	Policy   string `yaml:"policy" json:"policy"`
	Dynamic  *bool  `yaml:"dynamic" json:"dynamic"`
}

// PopBargeBlock is barge fleet settings for a PoP.
type PopBargeBlock struct {
	Replicas int    `yaml:"replicas" json:"replicas"`
	Runtime  string `yaml:"runtime" json:"runtime"`
	FleetID  string `yaml:"fleet_id" json:"fleet_id"`
	LB       string `yaml:"lb" json:"lb"` // register-lb host:port
	Host     string `yaml:"host" json:"host"`
}

// PopKernel is per-PoP kernel replication nodes (real-time sync peers).
type PopKernel struct {
	UltimateDB       *KernelNode `yaml:"ultimate_db" json:"ultimate_db"`
	UltimateKeystore *KernelNode `yaml:"ultimate_keystore" json:"ultimate_keystore"`
}

// KernelNode is one kernel barge identity + how other PoPs reach it.
type KernelNode struct {
	NodeID  string `yaml:"node_id" json:"node_id"`
	Listen  string `yaml:"listen" json:"listen"`
	DataDir string `yaml:"data_dir" json:"data_dir"`
	// Peers overrides auto mesh expansion when set (id=url,id2=url).
	Peers string `yaml:"peers" json:"peers"`
	// URL is how remote PoPs dial this node for replication (required for auto mesh).
	URL string `yaml:"url" json:"url"`
}

// ProcessProfile selects a single-process run when not using multi-pop roles.
type ProcessProfile struct {
	Mode string `yaml:"mode" json:"mode"`
	Pop  string `yaml:"pop" json:"pop"`
}

// FileOrInline is either a path to YAML or an inline object (marshaled later).
type FileOrInline struct {
	File   string         `yaml:"file" json:"file"`
	Inline map[string]any `yaml:"inline" json:"inline"`
}

// SiteRunPlan is the expanded, PoP-selected plan ready to apply or print.
type SiteRunPlan struct {
	PopID            string
	Roles            []string
	Domain           string
	PublicScheme     string
	Stack            *StackConfigFile
	StackPath        string // written temp path when applied
	AnycastPath      string
	VHostsPath       string
	DNSPath          string
	KernelDBPeers    string
	KernelKSPeers    string
	KernelDBNodeID   string
	KernelKSNodeID   string
	UDBListen        string
	UKSListen        string
	UDBDataDir       string
	UKSDataDir       string
	SuggestedMode    string
	Mesh             *SiteMeshBlock
	Hub              *SiteHubBlock
	Tunnel           *TunnelBlock
	TokenEnv         string
	BaseDir          string
}

// loadSiteConfigFile loads YAML/JSON site config (not .tug — use loadSiteAny).
func loadSiteConfigFile(path string) (*SiteConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty site config path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read site config %s: %w", path, err)
	}
	var cfg SiteConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		// try JSON
		if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
			return nil, fmt.Errorf("parse site config %s: %w", path, err)
		}
	}
	if err := normalizeSiteConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// loadSiteAny loads YAML/JSON or Tugconf (.tug / .set / set-style content).
func loadSiteAny(path string) (*SiteConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty site config path")
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".tug", ".set":
		return loadTugconfFile(path)
	}
	// Peek: if first non-comment non-empty line starts with set/delete/load, treat as tugconf.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if looksLikeTugconf(string(raw)) {
		return parseTugconf(string(raw), filepath.Dir(path))
	}
	return loadSiteConfigFile(path)
}

func looksLikeTugconf(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		low := strings.ToLower(line)
		return strings.HasPrefix(low, "set ") ||
			strings.HasPrefix(low, "delete ") ||
			strings.HasPrefix(low, "load ")
	}
	return false
}

func normalizeSiteConfig(cfg *SiteConfig) error {
	if cfg == nil {
		return fmt.Errorf("nil site config")
	}
	if strings.TrimSpace(cfg.APIVersion) == "" {
		cfg.APIVersion = siteAPIVersion
	}
	if strings.TrimSpace(cfg.Kind) == "" {
		cfg.Kind = siteKind
	}
	if cfg.KernelMesh != nil {
		m := strings.ToLower(strings.TrimSpace(cfg.KernelMesh.Mode))
		if m == "" {
			m = "full-mesh"
		}
		switch m {
		case "full-mesh", "fullmesh", "mesh":
			cfg.KernelMesh.Mode = "full-mesh"
		case "hub-spoke", "hubspoke", "star":
			cfg.KernelMesh.Mode = "hub-spoke"
		case "manual":
			cfg.KernelMesh.Mode = "manual"
		default:
			return fmt.Errorf("kernel_mesh.mode must be full-mesh, hub-spoke, or manual (got %q)", cfg.KernelMesh.Mode)
		}
	}
	seen := map[string]bool{}
	for i := range cfg.Pops {
		id := strings.ToLower(strings.TrimSpace(cfg.Pops[i].ID))
		if id == "" {
			return fmt.Errorf("pops[%d]: id is required", i)
		}
		if seen[id] {
			return fmt.Errorf("duplicate pop id %q", id)
		}
		seen[id] = true
		cfg.Pops[i].ID = id
		for j, r := range cfg.Pops[i].Roles {
			cfg.Pops[i].Roles[j] = strings.ToLower(strings.TrimSpace(r))
		}
	}
	return nil
}

// validateSiteConfig checks structural rules (before pop expand).
func validateSiteConfig(cfg *SiteConfig) error {
	if err := normalizeSiteConfig(cfg); err != nil {
		return err
	}
	if cfg.KernelMesh != nil && cfg.KernelMesh.Mode == "hub-spoke" {
		hub := strings.ToLower(strings.TrimSpace(cfg.KernelMesh.HubPop))
		if hub == "" {
			return fmt.Errorf("kernel_mesh.hub_pop required when mode is hub-spoke")
		}
		found := false
		for _, p := range cfg.Pops {
			if p.ID == hub {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("kernel_mesh.hub_pop %q not in pops", hub)
		}
	}
	return nil
}

// findPop returns the pop by id.
func findPop(cfg *SiteConfig, id string) (*PopConfig, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		if cfg.Process != nil && strings.TrimSpace(cfg.Process.Pop) != "" {
			id = strings.ToLower(strings.TrimSpace(cfg.Process.Pop))
		}
	}
	if id == "" {
		if len(cfg.Pops) == 1 {
			return &cfg.Pops[0], nil
		}
		if len(cfg.Pops) == 0 {
			return nil, nil // single-process site without pops
		}
		return nil, fmt.Errorf("site has %d pops; pass -pop <id>", len(cfg.Pops))
	}
	for i := range cfg.Pops {
		if cfg.Pops[i].ID == id {
			return &cfg.Pops[i], nil
		}
	}
	return nil, fmt.Errorf("unknown pop %q", id)
}

// resolveFileOrInline returns an absolute path to a YAML file (temp if inline).
func resolveFileOrInline(baseDir string, fi *FileOrInline, prefix string) (string, error) {
	if fi == nil {
		return "", nil
	}
	if f := strings.TrimSpace(fi.File); f != "" {
		if !filepath.IsAbs(f) {
			f = filepath.Join(baseDir, f)
		}
		if _, err := os.Stat(f); err != nil {
			return "", fmt.Errorf("%s file %s: %w", prefix, f, err)
		}
		return f, nil
	}
	if len(fi.Inline) == 0 {
		return "", nil
	}
	raw, err := yaml.Marshal(fi.Inline)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "tunneltug-"+prefix+"-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return tmp.Name(), nil
}

// siteConfigPath returns -config or TUNNELTUG_CONFIG (after env applied).
func siteConfigPath() string {
	return strings.TrimSpace(*siteConfigFlag)
}

// popIDFlagValue returns selected pop id.
func popIDFlagValue() string {
	return strings.ToLower(strings.TrimSpace(*sitePopFlag))
}
