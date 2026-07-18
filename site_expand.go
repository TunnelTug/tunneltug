package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ExpandSite builds a SiteRunPlan for the selected pop (or process-only site).
// Kernel peers are auto-wired for real-time replication across global ingresses.
func ExpandSite(cfg *SiteConfig, popID, baseDir string) (*SiteRunPlan, error) {
	if err := validateSiteConfig(cfg); err != nil {
		return nil, err
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		baseDir = "."
	}

	plan := &SiteRunPlan{
		BaseDir:      baseDir,
		TokenEnv:     strings.TrimSpace(cfg.Site.TokenEnv),
		Mesh:         cfg.Mesh,
		Hub:          cfg.Hub,
		PublicScheme: strings.TrimSpace(cfg.Site.PublicScheme),
		Domain:       strings.TrimSpace(cfg.Site.Domain),
	}
	if plan.PublicScheme == "" && plan.Domain != "" {
		plan.PublicScheme = "https"
	}
	if plan.TokenEnv == "" {
		plan.TokenEnv = "TUNNELTUG_TOKEN"
	}

	pop, err := findPop(cfg, popID)
	if err != nil {
		return nil, err
	}

	if pop != nil {
		plan.PopID = pop.ID
		plan.Roles = append([]string{}, pop.Roles...)
		if d := strings.TrimSpace(pop.Domain); d != "" {
			plan.Domain = d
		}
		plan.Tunnel = pop.Tunnel
		plan.SuggestedMode = suggestModeFromRoles(pop.Roles, cfg)

		// Resolve sidecar paths relative to site baseDir.
		if p, err := resolveFileOrInline(baseDir, pop.Anycast, "anycast"); err != nil {
			return nil, err
		} else {
			plan.AnycastPath = p
		}
		if p, err := resolveFileOrInline(baseDir, pop.VHosts, "vhosts"); err != nil {
			return nil, err
		} else {
			plan.VHostsPath = p
		}
		if p, err := resolveFileOrInline(baseDir, pop.DNS, "dns"); err != nil {
			return nil, err
		} else {
			plan.DNSPath = p
		}

		// Kernel node identity for this pop.
		if pop.Kernel != nil {
			if n := pop.Kernel.UltimateDB; n != nil {
				plan.KernelDBNodeID = strings.TrimSpace(n.NodeID)
				if plan.KernelDBNodeID == "" {
					plan.KernelDBNodeID = "udb-" + pop.ID
				}
				plan.UDBListen = strings.TrimSpace(n.Listen)
				plan.UDBDataDir = strings.TrimSpace(n.DataDir)
				if strings.TrimSpace(n.Peers) != "" {
					plan.KernelDBPeers = strings.TrimSpace(n.Peers)
				}
			}
			if n := pop.Kernel.UltimateKeystore; n != nil {
				plan.KernelKSNodeID = strings.TrimSpace(n.NodeID)
				if plan.KernelKSNodeID == "" {
					plan.KernelKSNodeID = "uks-" + pop.ID
				}
				plan.UKSListen = strings.TrimSpace(n.Listen)
				plan.UKSDataDir = strings.TrimSpace(n.DataDir)
				if strings.TrimSpace(n.Peers) != "" {
					plan.KernelKSPeers = strings.TrimSpace(n.Peers)
				}
			}
		}

		// Auto-expand peers from other pops when not manually set.
		if plan.KernelDBPeers == "" {
			plan.KernelDBPeers = expandKernelPeers(cfg, pop.ID, "db")
		}
		if plan.KernelKSPeers == "" {
			plan.KernelKSPeers = expandKernelPeers(cfg, pop.ID, "ks")
		}
	} else {
		// No pops: process profile only.
		if cfg.Process != nil {
			plan.SuggestedMode = strings.TrimSpace(cfg.Process.Mode)
		}
	}

	// Materialize stack with kernel node_id/peers injected for this pop.
	stack, err := materializeSiteStack(cfg, pop, plan)
	if err != nil {
		return nil, err
	}
	plan.Stack = stack

	return plan, nil
}

func suggestModeFromRoles(roles []string, cfg *SiteConfig) string {
	if cfg.Process != nil && strings.TrimSpace(cfg.Process.Mode) != "" {
		return strings.TrimSpace(cfg.Process.Mode)
	}
	has := map[string]bool{}
	for _, r := range roles {
		has[r] = true
	}
	// Prefer explicit stack/kernel/barge over tunnel edge.
	switch {
	case has["stack"]:
		return "stack"
	case has["kernel"] && !has["barge"] && !has["lb"] && !has["server"]:
		return "ultimate_db"
	case has["barge"]:
		return "barge"
	case has["lb"]:
		return "lb"
	case has["orchestrator"]:
		return "orchestrator"
	case has["anycast"] && !has["server"] && !has["lb"]:
		return "anycast"
	case has["server"]:
		return "server"
	case has["hub"]:
		return "hub"
	case has["client"]:
		return "client"
	default:
		return ""
	}
}

// expandKernelPeers builds id=url,id2=url for other PoPs (real-time replication mesh).
// kind is "db" or "ks".
func expandKernelPeers(cfg *SiteConfig, selfPopID, kind string) string {
	if cfg == nil || len(cfg.Pops) == 0 {
		return ""
	}
	mode := "full-mesh"
	hubPop := ""
	if cfg.KernelMesh != nil {
		if m := strings.TrimSpace(cfg.KernelMesh.Mode); m != "" {
			mode = m
		}
		hubPop = strings.ToLower(strings.TrimSpace(cfg.KernelMesh.HubPop))
	}
	if mode == "manual" {
		return ""
	}

	selfPopID = strings.ToLower(strings.TrimSpace(selfPopID))
	var parts []string

	for _, p := range cfg.Pops {
		if p.ID == selfPopID {
			continue
		}
		if p.Kernel == nil {
			continue
		}
		var node *KernelNode
		switch kind {
		case "db":
			node = p.Kernel.UltimateDB
		case "ks":
			node = p.Kernel.UltimateKeystore
		}
		if node == nil {
			continue
		}
		url := strings.TrimSpace(node.URL)
		if url == "" {
			continue
		}
		id := strings.TrimSpace(node.NodeID)
		if id == "" {
			prefix := "udb-"
			if kind == "ks" {
				prefix = "uks-"
			}
			id = prefix + p.ID
		}

		include := false
		switch mode {
		case "full-mesh":
			include = true
		case "hub-spoke":
			// Spokes peer only with hub; hub peers with all spokes.
			if selfPopID == hubPop {
				include = true // hub gets every other node
			} else if p.ID == hubPop {
				include = true // spoke gets hub only
			}
		}
		if include {
			parts = append(parts, id+"="+url)
		}
	}
	return strings.Join(parts, ",")
}

// materializeSiteStack merges site.stack + pop overrides and injects kernel peer fields.
func materializeSiteStack(cfg *SiteConfig, pop *PopConfig, plan *SiteRunPlan) (*StackConfigFile, error) {
	if cfg.Stack == nil && (pop == nil || pop.StackOverrides == nil) {
		return nil, nil
	}
	out := &StackConfigFile{}
	if cfg.Stack != nil {
		*out = *cfg.Stack
		// Deep-ish copy barge slices so we don't mutate source.
		if len(cfg.Stack.Barges) > 0 {
			out.Barges = append([]BargeProductConfig{}, cfg.Stack.Barges...)
			out.Products = nil
		} else if len(cfg.Stack.Products) > 0 {
			out.Products = append([]BargeProductConfig{}, cfg.Stack.Products...)
		}
	}
	if pop != nil && pop.StackOverrides != nil {
		mergeStackFile(out, pop.StackOverrides)
	}

	// Site / pop domain into stack.
	if strings.TrimSpace(out.Domain) == "" && strings.TrimSpace(plan.Domain) != "" {
		out.Domain = plan.Domain
	}
	if strings.TrimSpace(out.PublicScheme) == "" && strings.TrimSpace(plan.PublicScheme) != "" {
		out.PublicScheme = plan.PublicScheme
	}
	if cfg.Hub != nil {
		if strings.TrimSpace(out.HubHost) == "" && strings.TrimSpace(cfg.Hub.Host) != "" {
			out.HubHost = cfg.Hub.Host
		}
		if strings.TrimSpace(out.Tag) == "" && strings.TrimSpace(cfg.Hub.Tag) != "" {
			out.Tag = cfg.Hub.Tag
		}
	}

	// Inject kernel node_id + peers into ultimate_db / ultimate_keystore barge entries.
	entries := out.Barges
	if len(entries) == 0 {
		entries = out.Products
	}
	for i := range entries {
		name := strings.ToLower(strings.TrimSpace(entries[i].Name))
		// Resolve aliases lightly.
		switch name {
		case "ultimate_db", "ultimate-db", "udb", "kernel-db":
			if plan.KernelDBNodeID != "" && strings.TrimSpace(entries[i].NodeID) == "" {
				entries[i].NodeID = plan.KernelDBNodeID
			}
			if plan.KernelDBPeers != "" && strings.TrimSpace(entries[i].Peers) == "" {
				entries[i].Peers = plan.KernelDBPeers
			}
		case "ultimate_keystore", "ultimate-keystore", "uks", "keystore", "kernel-keystore":
			if plan.KernelKSNodeID != "" && strings.TrimSpace(entries[i].NodeID) == "" {
				entries[i].NodeID = plan.KernelKSNodeID
			}
			if plan.KernelKSPeers != "" && strings.TrimSpace(entries[i].Peers) == "" {
				entries[i].Peers = plan.KernelKSPeers
			}
		}
	}
	if len(out.Barges) > 0 {
		out.Barges = entries
	} else {
		out.Products = entries
	}

	// Rewrite relative barge file: paths to be absolute against site baseDir/stack context.
	// When stack is written to a temp file, file: includes must still resolve — fix to abs.
	rewriteBargeFilePaths(out, plan.BaseDir)

	return out, nil
}

func rewriteBargeFilePaths(stack *StackConfigFile, baseDir string) {
	if stack == nil {
		return
	}
	fix := func(list []BargeProductConfig) {
		for i := range list {
			if f := strings.TrimSpace(list[i].File); f != "" && !filepath.IsAbs(f) {
				// Prefer baseDir; stack files often live under config/
				list[i].File = filepath.Join(baseDir, f)
			}
			if cf := strings.TrimSpace(list[i].ConfigFile); cf != "" && !filepath.IsAbs(cf) {
				list[i].ConfigFile = filepath.Join(baseDir, cf)
			}
		}
	}
	fix(stack.Barges)
	fix(stack.Products)
}

// mergeStackFile overlays override fields onto base (non-empty wins).
func mergeStackFile(base, over *StackConfigFile) {
	if base == nil || over == nil {
		return
	}
	if strings.TrimSpace(over.Namespace) != "" {
		base.Namespace = over.Namespace
	}
	if strings.TrimSpace(over.Tag) != "" {
		base.Tag = over.Tag
	}
	if strings.TrimSpace(over.HubHost) != "" {
		base.HubHost = over.HubHost
	}
	if strings.TrimSpace(over.Domain) != "" {
		base.Domain = over.Domain
	}
	if strings.TrimSpace(over.PublicScheme) != "" {
		base.PublicScheme = over.PublicScheme
	}
	// If override lists barges, replace (SRE full override for that pop).
	if len(over.Barges) > 0 {
		base.Barges = append([]BargeProductConfig{}, over.Barges...)
		base.Products = nil
	} else if len(over.Products) > 0 {
		base.Products = append([]BargeProductConfig{}, over.Products...)
		base.Barges = nil
	}
}

// FormatSiteRunPlan is a human-readable check output.
func FormatSiteRunPlan(plan *SiteRunPlan) string {
	if plan == nil {
		return "(nil plan)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "site run plan\n")
	fmt.Fprintf(&b, "  pop:            %s\n", emptyDash(plan.PopID))
	fmt.Fprintf(&b, "  roles:          %s\n", emptyDash(strings.Join(plan.Roles, ",")))
	fmt.Fprintf(&b, "  domain:         %s\n", emptyDash(plan.Domain))
	fmt.Fprintf(&b, "  public_scheme:  %s\n", emptyDash(plan.PublicScheme))
	fmt.Fprintf(&b, "  suggested_mode: %s\n", emptyDash(plan.SuggestedMode))
	fmt.Fprintf(&b, "  token_env:      %s\n", emptyDash(plan.TokenEnv))
	fmt.Fprintf(&b, "  anycast:        %s\n", emptyDash(plan.AnycastPath))
	fmt.Fprintf(&b, "  vhosts:         %s\n", emptyDash(plan.VHostsPath))
	fmt.Fprintf(&b, "  dns:            %s\n", emptyDash(plan.DNSPath))
	fmt.Fprintf(&b, "  kernel_db:\n")
	fmt.Fprintf(&b, "    node_id: %s\n", emptyDash(plan.KernelDBNodeID))
	fmt.Fprintf(&b, "    peers:   %s\n", emptyDash(plan.KernelDBPeers))
	fmt.Fprintf(&b, "  kernel_ks:\n")
	fmt.Fprintf(&b, "    node_id: %s\n", emptyDash(plan.KernelKSNodeID))
	fmt.Fprintf(&b, "    peers:   %s\n", emptyDash(plan.KernelKSPeers))
	if plan.Stack != nil {
		n := len(plan.Stack.Barges)
		if n == 0 {
			n = len(plan.Stack.Products)
		}
		fmt.Fprintf(&b, "  stack_barges:   %d\n", n)
		fmt.Fprintf(&b, "  stack_ns:       %s\n", emptyDash(plan.Stack.Namespace))
	} else {
		fmt.Fprintf(&b, "  stack:          (none)\n")
	}
	if plan.Tunnel != nil {
		fmt.Fprintf(&b, "  tunnel:\n")
		fmt.Fprintf(&b, "    public:  %s\n", emptyDash(plan.Tunnel.Public))
		fmt.Fprintf(&b, "    control: %s\n", emptyDash(plan.Tunnel.Control))
		if plan.Tunnel.Barge != nil {
			fmt.Fprintf(&b, "    barge_replicas: %d\n", plan.Tunnel.Barge.Replicas)
			fmt.Fprintf(&b, "    barge_runtime:  %s\n", emptyDash(plan.Tunnel.Barge.Runtime))
			fmt.Fprintf(&b, "    fleet_id:       %s\n", emptyDash(plan.Tunnel.Barge.FleetID))
		}
	}
	fmt.Fprintf(&b, "\n# Kernel peers are for real-time replication across global ingresses.\n")
	fmt.Fprintf(&b, "# Local embeds stay primary; AddPeer these URLs — do not prefer-remote.\n")
	return b.String()
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
