package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Stack / barge YAML config — SRE flow:
//
//	tunneltug -mode stack -stack-config config/stack.yaml
//	tunneltug -mode barge -k3s-stack -barge-config config/stack.yaml
//
// Each product barge is configurable: replicas, env, image tag, optional
// config file mounted at /config in the pod.

// stackConfigPath returns -stack-config or -barge-config (SRE alias).
func stackConfigPath() string {
	if p := strings.TrimSpace(*stackConfig); p != "" {
		return p
	}
	return strings.TrimSpace(*bargeConfig)
}

// StackConfigFile is the root document for -stack-config.
type StackConfigFile struct {
	// Namespace for all stack Deployments/Services (default: 0trust-stack).
	Namespace string `yaml:"namespace" json:"namespace"`
	// Tag default for all products without their own image/tag (default: -stack-tag / -hub-tag).
	Tag string `yaml:"tag" json:"tag"`
	// HubHost registry host without scheme (default: hub.tunneltug.com).
	HubHost string `yaml:"hub_host" json:"hub_host"`
	// Domain is the stack-wide public DNS base (e.g. example.com). Never hardcoded —
	// omit for in-cluster-only URLs; set so each barge defaults to
	// {public_scheme}://{name}.{domain} unless it has its own domain/public_url.
	Domain string `yaml:"domain" json:"domain"`
	// PublicScheme is http or https for domain-derived public URLs (default https when domain set).
	PublicScheme string `yaml:"public_scheme" json:"public_scheme"`
	// Products (barges) to run. Each entry merges over the built-in catalog.
	Products []BargeProductConfig `yaml:"products" json:"products"`
	// Barges is an alias for products (SRE wording).
	Barges []BargeProductConfig `yaml:"barges" json:"barges"`
}

// BargeProductConfig is one configurable product barge.
type BargeProductConfig struct {
	// Name is the catalog product key (williwaw, auth, name, orchid_ingest, …).
	Name string `yaml:"name" json:"name"`
	// File is optional path to a single-barge YAML (relative to stack config dir).
	// If set, that file is loaded and merged (file values win, then this struct).
	File string `yaml:"file" json:"file"`
	// Replicas for the Deployment (default 1).
	Replicas *int32 `yaml:"replicas" json:"replicas"`
	// Port overrides the catalog listen port.
	Port *int32 `yaml:"port" json:"port"`
	// Image full ref override (else hub_host/repo:tag).
	Image string `yaml:"image" json:"image"`
	// Tag overrides stack-level tag for this product only.
	Tag string `yaml:"tag" json:"tag"`
	// Domain is this barge's public hostname (e.g. williwaw.example.com).
	// Overrides stack domain for PUBLIC_* URLs. Not hardcoded in code.
	Domain string `yaml:"domain" json:"domain"`
	// PublicURL is a full public base URL override (e.g. https://williwaw.example.com).
	// Wins over domain when set.
	PublicURL string `yaml:"public_url" json:"public_url"`
	// Env merges over catalog defaults (env wins over generated link env).
	Env map[string]string `yaml:"env" json:"env"`
	// ConfigFile is a host path to YAML/JSON/env file mounted into the pod at ConfigMount.
	// Content is loaded into a ConfigMap named {name}-config.
	ConfigFile string `yaml:"config_file" json:"config_file"`
	// ConfigMount is the in-pod directory for ConfigFile (default /config).
	ConfigMount string `yaml:"config_mount" json:"config_mount"`
	// ConfigKey is the filename inside the mount (default: basename of ConfigFile, or config.yaml).
	ConfigKey string `yaml:"config_key" json:"config_key"`
	// NodeID is the kernel replication node id (ultimate_db / ultimate_keystore).
	NodeID string `yaml:"node_id" json:"node_id"`
	// Peers is kernel replication peers "id=url,id2=url" for scatter-gather across kernel barges.
	Peers string `yaml:"peers" json:"peers"`
	// Disabled skips this barge when true.
	Disabled bool `yaml:"disabled" json:"disabled"`
}

// resolvedStack is the fully merged stack ready to reconcile.
type resolvedStack struct {
	Namespace    string
	Tag          string
	HubHost      string
	Domain       string
	PublicScheme string
	Apps         []stackApp
}

// loadStackConfig reads YAML/JSON from path and returns a resolved stack.
// baseDir is used to resolve relative file: and config_file: paths (usually dir of path).
func loadStackConfig(path string) (*resolvedStack, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty stack config path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read stack config %s: %w", path, err)
	}
	var file StackConfigFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse stack config %s: %w", path, err)
	}
	baseDir := filepath.Dir(path)
	return resolveStackConfig(file, baseDir)
}

func resolveStackConfig(file StackConfigFile, baseDir string) (*resolvedStack, error) {
	entries := file.Products
	if len(entries) == 0 {
		entries = file.Barges
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("stack config: no products/barges listed")
	}

	ns := strings.TrimSpace(file.Namespace)
	if ns == "" {
		ns = "0trust-stack"
	}
	tag := strings.TrimSpace(file.Tag)
	if tag == "" {
		tag = strings.TrimSpace(*stackTag)
	}
	if tag == "" {
		tag = strings.TrimSpace(*hubTag)
	}
	if tag == "" {
		tag = "latest"
	}
	hub := strings.TrimSpace(file.HubHost)
	if hub == "" {
		hub = strings.TrimSpace(*hubHost)
	}
	if hub == "" {
		hub = "hub.tunneltug.com"
	}
	hub = strings.TrimPrefix(strings.TrimPrefix(hub, "https://"), "http://")

	stackDomain := strings.TrimSpace(file.Domain)
	publicScheme := strings.ToLower(strings.TrimSpace(file.PublicScheme))
	if publicScheme == "" {
		if stackDomain != "" {
			publicScheme = "https"
		} else {
			publicScheme = "http"
		}
	}
	if publicScheme != "http" && publicScheme != "https" {
		return nil, fmt.Errorf("stack config: public_scheme must be http or https, got %q", publicScheme)
	}

	cat := stackCatalog()
	var apps []stackApp
	seen := map[string]bool{}

	for _, entry := range entries {
		merged, err := loadBargeEntry(entry, baseDir)
		if err != nil {
			return nil, err
		}
		if merged.Disabled {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(merged.Name))
		if name == "" {
			return nil, fmt.Errorf("stack config: product missing name")
		}
		// Resolve aliases via hub catalog.
		if prod, err := resolveHubProduct(name); err == nil {
			name = prod.Name
		}
		switch name {
		case "tunneltug", "engine", "barge":
			continue // engine is barge fleet STS, not product stack
		case "dbsc-relay":
			name = "dbsc_relay"
		case "orchid-ingest", "orchid", "orchid_sync", "orchid_sync_ingest":
			name = "orchid_ingest"
		case "idp", "identity":
			name = "auth"
		case "ztna":
			name = "access"
		case "service-keys", "service_keys", "keys":
			name = "servicekeys"
		case "name-service", "name_service":
			name = "nameservice"
		case "elastic", "observability":
			name = "logs"
		case "workflow":
			name = "workflows"
		case "ultimate-db", "udb", "kernel-db":
			name = "ultimate_db"
		case "ultimate-keystore", "uks", "keystore", "kernel-keystore":
			name = "ultimate_keystore"
		}

		base, ok := cat[name]
		if !ok {
			return nil, fmt.Errorf("stack config: unknown product %q", name)
		}
		if seen[base.Name] {
			continue
		}
		seen[base.Name] = true

		app := applyBargeOverrides(base, merged, tag, hub, stackDomain, publicScheme)
		apps = append(apps, app)
	}
	if len(apps) == 0 {
		return nil, fmt.Errorf("stack config: no enabled products after resolve")
	}
	return &resolvedStack{
		Namespace:    ns,
		Tag:          tag,
		HubHost:      hub,
		Domain:       stackDomain,
		PublicScheme: publicScheme,
		Apps:         apps,
	}, nil
}

func loadBargeEntry(entry BargeProductConfig, baseDir string) (BargeProductConfig, error) {
	out := entry
	if f := strings.TrimSpace(entry.File); f != "" {
		if !filepath.IsAbs(f) {
			f = filepath.Join(baseDir, f)
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return out, fmt.Errorf("barge file %s: %w", f, err)
		}
		var fromFile BargeProductConfig
		if err := yaml.Unmarshal(raw, &fromFile); err != nil {
			return out, fmt.Errorf("parse barge file %s: %w", f, err)
		}
		// Entry fields override file.
		out = mergeBargeConfig(fromFile, entry)
		// Resolve config_file relative to the barge file's directory when set in file.
		if out.ConfigFile != "" && !filepath.IsAbs(out.ConfigFile) {
			// Prefer entry baseDir if entry re-specified; else file dir.
			cfgBase := filepath.Dir(f)
			if strings.TrimSpace(entry.ConfigFile) != "" {
				cfgBase = baseDir
			}
			out.ConfigFile = filepath.Join(cfgBase, out.ConfigFile)
		}
	} else if out.ConfigFile != "" && !filepath.IsAbs(out.ConfigFile) {
		out.ConfigFile = filepath.Join(baseDir, out.ConfigFile)
	}
	return out, nil
}

func mergeBargeConfig(base, over BargeProductConfig) BargeProductConfig {
	out := base
	if strings.TrimSpace(over.Name) != "" {
		out.Name = over.Name
	}
	if over.Replicas != nil {
		out.Replicas = over.Replicas
	}
	if over.Port != nil {
		out.Port = over.Port
	}
	if strings.TrimSpace(over.Image) != "" {
		out.Image = over.Image
	}
	if strings.TrimSpace(over.Tag) != "" {
		out.Tag = over.Tag
	}
	if strings.TrimSpace(over.Domain) != "" {
		out.Domain = over.Domain
	}
	if strings.TrimSpace(over.PublicURL) != "" {
		out.PublicURL = over.PublicURL
	}
	if strings.TrimSpace(over.ConfigFile) != "" {
		out.ConfigFile = over.ConfigFile
	}
	if strings.TrimSpace(over.ConfigMount) != "" {
		out.ConfigMount = over.ConfigMount
	}
	if strings.TrimSpace(over.ConfigKey) != "" {
		out.ConfigKey = over.ConfigKey
	}
	if strings.TrimSpace(over.NodeID) != "" {
		out.NodeID = over.NodeID
	}
	if strings.TrimSpace(over.Peers) != "" {
		out.Peers = over.Peers
	}
	if over.Disabled {
		out.Disabled = true
	}
	if len(over.Env) > 0 {
		if out.Env == nil {
			out.Env = map[string]string{}
		}
		for k, v := range over.Env {
			out.Env[k] = v
		}
	}
	// Don't inherit File into recursive loads.
	out.File = ""
	return out
}

func applyBargeOverrides(base stackApp, cfg BargeProductConfig, defaultTag, hubHost, stackDomain, publicScheme string) stackApp {
	app := base
	// Copy env map so we don't mutate catalog.
	env := map[string]string{}
	for k, v := range base.Env {
		env[k] = v
	}
	for k, v := range cfg.Env {
		env[k] = v
	}
	app.Env = env

	if cfg.Port != nil && *cfg.Port > 0 {
		app.Port = *cfg.Port
	}
	// Align process listen env with Service/container port (catalog zero-config default or YAML port:).
	syncStackListenEnv(&app)
	if cfg.Replicas != nil && *cfg.Replicas > 0 {
		app.Replicas = *cfg.Replicas
	} else if app.Replicas <= 0 {
		app.Replicas = 1
	}

	tag := strings.TrimSpace(cfg.Tag)
	if tag == "" {
		tag = defaultTag
	}
	if img := strings.TrimSpace(cfg.Image); img != "" {
		app.ImageOverride = img
	} else {
		app.ImageOverride = ""
		app.TagOverride = tag
	}
	app.HubHostOverride = hubHost
	app.StackDomain = strings.TrimSpace(stackDomain)
	app.PublicScheme = strings.TrimSpace(publicScheme)
	if app.PublicScheme == "" {
		app.PublicScheme = "https"
	}
	app.Domain = strings.TrimSpace(cfg.Domain)
	app.PublicURL = strings.TrimSpace(cfg.PublicURL)
	app.NodeID = strings.TrimSpace(cfg.NodeID)
	app.Peers = strings.TrimSpace(cfg.Peers)

	if cf := strings.TrimSpace(cfg.ConfigFile); cf != "" {
		app.ConfigFile = cf
		app.ConfigMount = strings.TrimSpace(cfg.ConfigMount)
		if app.ConfigMount == "" {
			app.ConfigMount = "/config"
		}
		app.ConfigKey = strings.TrimSpace(cfg.ConfigKey)
		if app.ConfigKey == "" {
			app.ConfigKey = filepath.Base(cf)
			if app.ConfigKey == "." || app.ConfigKey == "/" || app.ConfigKey == "" {
				app.ConfigKey = "config.yaml"
			}
		}
	}
	return app
}

// resolveBargePublicURL picks the public base URL for a barge.
// Priority: explicit public_url → barge domain → name.stack_domain → in-cluster service DNS.
// Domains are never hardcoded in TunnelTug; only YAML / flags supply them.
func resolveBargePublicURL(app stackApp, ns string) string {
	if u := strings.TrimSpace(app.PublicURL); u != "" {
		return strings.TrimRight(u, "/")
	}
	scheme := strings.ToLower(strings.TrimSpace(app.PublicScheme))
	if scheme == "" {
		scheme = "https"
	}
	if d := strings.TrimSpace(app.Domain); d != "" {
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		d = strings.TrimRight(d, "/")
		return scheme + "://" + d
	}
	if sd := strings.TrimSpace(app.StackDomain); sd != "" {
		sd = strings.TrimPrefix(strings.TrimPrefix(sd, "https://"), "http://")
		sd = strings.TrimRight(sd, "/")
		// Platform-style faces often use a single stack domain; product apps get name.stack_domain.
		if isApexProductName(app.Name) {
			return scheme + "://" + sd
		}
		return scheme + "://" + app.Name + "." + sd
	}
	p := app.Port
	if p <= 0 {
		p = 80
	}
	return fmt.Sprintf("http://%s.%s.svc:%d", app.Name, ns, p)
}

// isApexProductName is true for single-tenant control-plane faces that sit on the stack apex domain.
func isApexProductName(name string) bool {
	switch name {
	case "platform", "services", "name", "auth", "iam", "access", "scim", "pki",
		"workflows", "topology", "nameservice", "servicekeys", "vpi", "logs", "orchid-ingest":
		return true
	default:
		return false
	}
}

// stackClusterServiceURL builds an in-cluster URL using the sibling's YAML/catalog port (never a hardcoded foreign port).
func stackClusterServiceURL(serviceName string, all []stackApp, ns string, defaultPort int32) string {
	serviceName = strings.TrimSpace(serviceName)
	for _, a := range all {
		if a.Name == serviceName {
			p := a.Port
			if p <= 0 {
				p = defaultPort
			}
			return fmt.Sprintf("http://%s.%s.svc:%d", serviceName, ns, p)
		}
	}
	if defaultPort <= 0 {
		defaultPort = 80
	}
	return fmt.Sprintf("http://%s.%s.svc:%d", serviceName, ns, defaultPort)
}

// syncStackListenEnv rewrites well-known listen env vars to match app.Port so
// YAML port: overrides keep Service, containerPort, and process bind in sync.
// Catalog values are the zero-config defaults shown on the hub as "Zero Config Port".
func syncStackListenEnv(app *stackApp) {
	if app == nil || app.Port <= 0 {
		return
	}
	if app.Env == nil {
		app.Env = map[string]string{}
	}
	port := app.Port
	listen := fmt.Sprintf(":%d", port)
	// Generic platform / TRUST faces
	if _, ok := app.Env["TRUST_PORT"]; ok || strings.HasPrefix(app.Component, "idp") ||
		app.Component == "control-plane" || app.Component == "gtld-face" ||
		app.Component == "vpi" || app.Component == "pki" || app.Component == "scim" ||
		app.Component == "iam" || app.Component == "ztna-access" || app.Component == "workflows" ||
		app.Component == "topology" || app.Component == "nameservice" || app.Component == "service-keys" ||
		app.Component == "elastic-logs" || app.Component == "orchid-ingest" {
		app.Env["TRUST_PORT"] = fmt.Sprintf("%d", port)
	}
	// Product-specific listen keys used by 0Trust images
	for _, key := range []string{
		"WILLIWAW_LISTEN", "MOTIONKB_LISTEN", "ACK_LISTEN", "MAIL_LISTEN",
		"SEARCH_LISTEN", "SOCIAL_LISTEN", "DBSC_RELAY_LISTEN",
	} {
		if _, ok := app.Env[key]; ok {
			app.Env[key] = listen
		}
	}
	// Always set listen for known product names even if catalog omitted the key.
	switch app.Name {
	case "williwaw":
		app.Env["WILLIWAW_LISTEN"] = listen
	case "motionkb":
		app.Env["MOTIONKB_LISTEN"] = listen
	case "ack":
		app.Env["ACK_LISTEN"] = listen
	case "mail":
		app.Env["MAIL_LISTEN"] = listen
	case "search":
		app.Env["SEARCH_LISTEN"] = listen
	case "social":
		app.Env["SOCIAL_LISTEN"] = listen
	case "dbsc-relay":
		app.Env["DBSC_RELAY_LISTEN"] = listen
	case "ultimate-db":
		// container args use -udb-listen from app.Port
	case "ultimate-keystore":
		// container args use -uks-listen from app.Port
	}
}
