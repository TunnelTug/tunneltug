package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ApplySitePlan applies an expanded SiteRunPlan into process flags.
// CLI-set flags always win (never overwritten).
func ApplySitePlan(plan *SiteRunPlan) error {
	if plan == nil {
		return nil
	}

	// Token from env name if site requests it and token still empty.
	if !flagWasSet("token") && strings.TrimSpace(*authToken) == "" {
		if env := strings.TrimSpace(plan.TokenEnv); env != "" {
			if v := strings.TrimSpace(os.Getenv(env)); v != "" && !isWeakToken(v) {
				*authToken = v
			}
		}
	}

	// Mode: only if not explicitly set (default is "client").
	if !flagWasSet("mode") && strings.TrimSpace(plan.SuggestedMode) != "" {
		*mode = plan.SuggestedMode
	}

	// Domain / public face.
	if !flagWasSet("domain") && strings.TrimSpace(*domain) == "" && strings.TrimSpace(plan.Domain) != "" {
		*domain = plan.Domain
	}

	// Tunnel ports and prod.
	if plan.Tunnel != nil {
		t := plan.Tunnel
		if !flagWasSet("public") && strings.TrimSpace(t.Public) != "" {
			*publicPort = strings.TrimSpace(t.Public)
		}
		if !flagWasSet("control") && strings.TrimSpace(t.Control) != "" {
			*controlPort = strings.TrimSpace(t.Control)
		}
		if !flagWasSet("prod") && t.Prod {
			*prod = true
		}
		if !flagWasSet("dev") && t.Dev {
			*dev = true
		}
		if !flagWasSet("email") && strings.TrimSpace(t.Email) != "" && strings.TrimSpace(*email) == "" {
			*email = t.Email
		}
		if !flagWasSet("routing") && strings.TrimSpace(t.Routing) != "" {
			*routing = t.Routing
		}
		if t.LB != nil {
			if !flagWasSet("backends") && strings.TrimSpace(t.LB.Backends) != "" && strings.TrimSpace(*lbBackends) == "" {
				*lbBackends = t.LB.Backends
			}
			if !flagWasSet("lb-policy") && strings.TrimSpace(t.LB.Policy) != "" {
				*lbPolicy = t.LB.Policy
			}
			if t.LB.Dynamic != nil && !flagWasSet("lb-dynamic") {
				*lbDynamic = *t.LB.Dynamic
			}
		}
		if t.Barge != nil {
			b := t.Barge
			if !flagWasSet("barge-replicas") && b.Replicas > 0 {
				*bargeReplicas = b.Replicas
			}
			if !flagWasSet("barge-runtime") && strings.TrimSpace(b.Runtime) != "" {
				*bargeRuntime = b.Runtime
			}
			if !flagWasSet("barge-fleet-id") && strings.TrimSpace(b.FleetID) != "" && strings.TrimSpace(*bargeFleetID) == "" {
				*bargeFleetID = b.FleetID
			}
			if !flagWasSet("barge-lb") && strings.TrimSpace(b.LB) != "" && strings.TrimSpace(*bargeLB) == "" {
				*bargeLB = b.LB
			}
			if !flagWasSet("barge-host") && strings.TrimSpace(b.Host) != "" {
				*bargeHost = b.Host
			}
		}
	}

	// Mesh.
	if plan.Mesh != nil {
		m := plan.Mesh
		if m.Enabled && !flagWasSet("mesh") {
			*meshEnabled = true
		}
		if !flagWasSet("mesh-tld") && strings.TrimSpace(m.TLD) != "" {
			*meshTLD = m.TLD
		}
		if !flagWasSet("mesh-zone") && strings.TrimSpace(m.Zone) != "" {
			*meshZone = m.Zone
		}
		if !flagWasSet("mesh-ns") && strings.TrimSpace(m.NS) != "" {
			*meshNSHost = m.NS
		}
		if !flagWasSet("mesh-edge-ip") && strings.TrimSpace(m.EdgeIP) != "" && strings.TrimSpace(*meshEdgeIP) == "" {
			*meshEdgeIP = m.EdgeIP
		}
		if !flagWasSet("mesh-data-dir") && strings.TrimSpace(m.DataDir) != "" && strings.TrimSpace(*meshDataDir) == "" {
			*meshDataDir = m.DataDir
		}
		if !flagWasSet("mesh-dns") && strings.TrimSpace(m.DNS) != "" {
			*meshDNS = m.DNS
		}
	}

	// Hub.
	if plan.Hub != nil {
		if !flagWasSet("hub-host") && strings.TrimSpace(plan.Hub.Host) != "" {
			*hubHost = plan.Hub.Host
		}
		if !flagWasSet("hub-tag") && strings.TrimSpace(plan.Hub.Tag) != "" {
			*hubTag = plan.Hub.Tag
		}
	}

	// Sidecar file paths.
	if !flagWasSet("anycast-config") && strings.TrimSpace(plan.AnycastPath) != "" && strings.TrimSpace(*anycastConfig) == "" {
		*anycastConfig = plan.AnycastPath
		if !flagWasSet("anycast") {
			// Enable sidecar when anycast config is present and mode is server/lb.
			// Standalone anycast mode is selected via roles/suggested mode.
			*anycastEnable = true
		}
	}
	if !flagWasSet("vhosts") && strings.TrimSpace(plan.VHostsPath) != "" && strings.TrimSpace(*vhostsFile) == "" {
		*vhostsFile = plan.VHostsPath
	}
	if !flagWasSet("dns") && strings.TrimSpace(plan.DNSPath) != "" && strings.TrimSpace(*dnsFileFlag) == "" {
		*dnsFileFlag = plan.DNSPath
	}

	// Kernel standalone flags.
	if !flagWasSet("udb-node-id") && strings.TrimSpace(plan.KernelDBNodeID) != "" {
		*udbNodeID = plan.KernelDBNodeID
	}
	if !flagWasSet("udb-peers") && strings.TrimSpace(plan.KernelDBPeers) != "" && strings.TrimSpace(*udbPeers) == "" {
		*udbPeers = plan.KernelDBPeers
	}
	if !flagWasSet("udb-listen") && strings.TrimSpace(plan.UDBListen) != "" {
		*udbListen = plan.UDBListen
	}
	if !flagWasSet("udb-data") && strings.TrimSpace(plan.UDBDataDir) != "" && strings.TrimSpace(*udbDataDir) == "" {
		*udbDataDir = plan.UDBDataDir
	}
	if !flagWasSet("uks-node-id") && strings.TrimSpace(plan.KernelKSNodeID) != "" {
		*uksNodeID = plan.KernelKSNodeID
	}
	if !flagWasSet("uks-listen") && strings.TrimSpace(plan.UKSListen) != "" {
		*uksListen = plan.UKSListen
	}
	if !flagWasSet("uks-data") && strings.TrimSpace(plan.UKSDataDir) != "" && strings.TrimSpace(*uksDataDir) == "" {
		*uksDataDir = plan.UKSDataDir
	}

	// Materialize stack YAML to a temp file and point -stack-config at it
	// when operator did not already pass stack/barge config.
	if plan.Stack != nil && !flagWasSet("stack-config") && !flagWasSet("barge-config") {
		if strings.TrimSpace(*stackConfig) == "" && strings.TrimSpace(*bargeConfig) == "" {
			path, err := writeStackTemp(plan.Stack)
			if err != nil {
				return fmt.Errorf("write expanded stack: %w", err)
			}
			plan.StackPath = path
			*stackConfig = path
			// When barge role present, also enable k3s-stack co-run if mode is barge.
			if hasRole(plan.Roles, "stack") && hasRole(plan.Roles, "barge") && !flagWasSet("k3s-stack") {
				*k3sStack = true
			}
		}
	}

	return nil
}

func writeStackTemp(stack *StackConfigFile) (string, error) {
	raw, err := yaml.Marshal(stack)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), "tunneltug-site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "stack-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func hasRole(roles []string, want string) bool {
	want = strings.ToLower(want)
	for _, r := range roles {
		if strings.ToLower(r) == want {
			return true
		}
	}
	return false
}

// flagWasSet reports whether the operator passed -name on the CLI.
func flagWasSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// maybeApplySiteConfig loads -config / TUNNELTUG_CONFIG, expands for -pop, applies.
// When -config-check or -config-show-set, prints and returns done=true (caller should exit).
func maybeApplySiteConfig() (done bool, err error) {
	path := siteConfigPath()
	if path == "" {
		return false, nil
	}

	cfg, err := loadSiteAny(path)
	if err != nil {
		return false, err
	}
	baseDir := filepath.Dir(path)
	popID := popIDFlagValue()
	plan, err := ExpandSite(cfg, popID, baseDir)
	if err != nil {
		return false, err
	}

	if *siteConfigShowSet {
		lines, err := SiteConfigToSetLines(cfg)
		if err != nil {
			return true, err
		}
		fmt.Print(lines)
		return true, nil
	}

	if *siteConfigCheck {
		fmt.Print(FormatSiteRunPlan(plan))
		// Also show set view of effective kernel peers for the pop.
		if plan.KernelDBPeers != "" {
			fmt.Printf("\n# expanded set-style kernel peers for pop %s\n", plan.PopID)
			fmt.Printf("set pop %s kernel ultimate_db peers %q\n", plan.PopID, plan.KernelDBPeers)
			if plan.KernelKSPeers != "" {
				fmt.Printf("set pop %s kernel ultimate_keystore peers %q\n", plan.PopID, plan.KernelKSPeers)
			}
		}
		return true, nil
	}

	if err := ApplySitePlan(plan); err != nil {
		return false, err
	}
	if !*quiet {
		if plan.PopID != "" {
			fmt.Fprintf(os.Stderr, "site config %s pop=%s mode=%s kernel_db_peers=%q\n",
				path, plan.PopID, *mode, plan.KernelDBPeers)
		} else {
			fmt.Fprintf(os.Stderr, "site config %s loaded\n", path)
		}
	}
	return false, nil
}
