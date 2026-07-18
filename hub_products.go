package main

import (
	"fmt"
	"strings"
)

// Product catalog for hub.tunneltug.com.
//
// 0Trust apps live under 0trust/<name>.
// The TunnelTug *engine* image (binary run inside k3s fleet pods) is tunneltug/engine.
//
// "Barge" is NOT a product. Barge is the TunnelTug mode that runs k3s
// (-mode barge -barge-runtime k3s): a fleet of pods, each running the engine image.
//
// Platform *feature faces* (auth, iam, access, …) are separate hub images that
// package the same platform binary with TRUST_PRODUCT branding — individual
// barges for each 0TrustCloud control-plane capability.

type hubProduct struct {
	Name     string // mail, search, platform, services, social, tunneltug
	HubRepo  string // 0trust/mail or tunneltug/engine
	LocalRef string // default local k3s image name before push
}

func hubProd(name, repo string) hubProduct {
	return hubProduct{Name: name, HubRepo: repo, LocalRef: repo}
}

func alias(canonical hubProduct, keys ...string) {
	for _, k := range keys {
		hubProductCatalog[k] = canonical
	}
}

var hubProductCatalog = map[string]hubProduct{}

func init() {
	// --- standalone 0Trust apps ---
	alias(hubProd("mail", "0trust/mail"), "mail")
	alias(hubProd("search", "0trust/search"), "search")
	alias(hubProd("platform", "0trust/platform"), "platform")
	alias(hubProd("services", "0trust/services"), "services")
	alias(hubProd("social", "0trust/social"), "social", "cdn")
	alias(hubProd("ack", "0trust/ack"), "ack", "defcon")
	alias(hubProd("motionkb", "0trust/motionkb"), "motionkb", "motion")
	alias(hubProd("williwaw", "0trust/williwaw"), "williwaw")

	// --- public / edge faces ---
	// 0Trust Name (gTLD) — ICANN brand face (DoH / RDAP / registrar).
	alias(hubProd("name", "0trust/name"), "name", "gtld", "0trust-name", "0trust_name")
	alias(hubProd("dbsc_relay", "0trust/dbsc-relay"), "dbsc_relay", "dbsc-relay", "dbsc")
	alias(hubProd("anycast", "0trust/anycast"), "anycast")

	// --- platform feature barges (same binary, TRUST_PRODUCT face) ---
	alias(hubProd("orchid_ingest", "0trust/orchid-ingest"),
		"orchid_ingest", "orchid-ingest", "orchid_sync_ingest", "orchid-sync-ingest",
		"orchid_sync", "orchid-sync", "orchid")
	alias(hubProd("auth", "0trust/auth"), "auth", "idp", "identity")
	alias(hubProd("iam", "0trust/iam"), "iam")
	alias(hubProd("access", "0trust/access"), "access", "ztna")
	alias(hubProd("scim", "0trust/scim"), "scim")
	alias(hubProd("pki", "0trust/pki"), "pki")
	alias(hubProd("workflows", "0trust/workflows"), "workflows", "workflow")
	alias(hubProd("topology", "0trust/topology"), "topology")
	alias(hubProd("nameservice", "0trust/nameservice"), "nameservice", "name-service", "name_service")
	alias(hubProd("servicekeys", "0trust/servicekeys"), "servicekeys", "service-keys", "service_keys", "keys")
	alias(hubProd("vpi", "0trust/vpi"), "vpi")
	alias(hubProd("logs", "0trust/logs"), "logs", "elastic", "observability")

	// --- kernel data-replication barges (service data plane; not mesh/SDF embeds) ---
	alias(hubProd("ultimate_db", "0trust/ultimate-db"),
		"ultimate_db", "ultimate-db", "udb", "kernel-db", "kernel_replication_db")
	alias(hubProd("ultimate_keystore", "0trust/ultimate-keystore"),
		"ultimate_keystore", "ultimate-keystore", "ultimate_store", "ultimate-store",
		"uks", "keystore", "kernel-keystore", "store", "kernel_replication_keystore")

	// --- TunnelTug engine (k3s fleet pods) ---
	alias(hubProd("tunneltug", "tunneltug/engine"), "tunneltug", "engine", "barge")
}

func listHubProductNames() []string {
	return []string{
		"mail", "search", "platform", "services", "social", "ack", "motionkb", "williwaw",
		"name", "dbsc_relay", "anycast", "orchid_ingest",
		"auth", "iam", "access", "scim", "pki", "workflows", "topology", "nameservice", "servicekeys", "vpi", "logs",
		"ultimate_db", "ultimate_keystore",
		"tunneltug",
	}
}

// HubProductCatalogPublic is a UI-friendly list for tunneltug.com hub page.
func HubProductCatalogPublic() []map[string]string {
	return []map[string]string{
		{"name": "ack", "display": "Ack", "repo": "0trust/ack", "desc": "Event and community chat"},
		{"name": "williwaw", "display": "Williwaw", "repo": "0trust/williwaw", "desc": "Social feed and stories"},
		{"name": "motionkb", "display": "MotionKB", "repo": "0trust/motionkb", "desc": "Docs and published sites"},
		{"name": "mail", "display": "MeshMail", "repo": "0trust/mail", "desc": "Secure team mail"},
		{"name": "search", "display": "MeshSearch", "repo": "0trust/search", "desc": "Private search"},
		{"name": "platform", "display": "0Trust Platform", "repo": "0trust/platform", "desc": "Full identity and control plane"},
		{"name": "services", "display": "0Trust Services", "repo": "0trust/services", "desc": "Mesh access edge"},
		{"name": "social", "display": "0Trust CDN", "repo": "0trust/social", "desc": "Media and object CDN"},
		{"name": "name", "display": "0Trust Name (gTLD)", "repo": "0trust/name", "desc": "Public gTLD face — DoH, RDAP, registrar brand"},
		{"name": "dbsc_relay", "display": "DBSC Relay", "repo": "0trust/dbsc-relay", "desc": "Device-bound session credential relay"},
		{"name": "anycast", "display": "Anycast edge", "repo": "0trust/anycast", "desc": "Multi-region DNS and BGP anycast"},
		{"name": "orchid_ingest", "display": "Orchid Sync Ingest", "repo": "0trust/orchid-ingest", "desc": "Log and event ingest with orchid_sync BM25"},
		{"name": "auth", "display": "0Trust Auth", "repo": "0trust/auth", "desc": "Identity provider — OIDC, passkeys, sessions"},
		{"name": "iam", "display": "0Trust IAM", "repo": "0trust/iam", "desc": "Users, roles, and admin IAM console"},
		{"name": "access", "display": "0Trust Access", "repo": "0trust/access", "desc": "ZTNA app access gateway"},
		{"name": "scim", "display": "0Trust SCIM", "repo": "0trust/scim", "desc": "SCIM provisioning plane"},
		{"name": "pki", "display": "0Trust PKI", "repo": "0trust/pki", "desc": "Mesh CA and certificate issuance"},
		{"name": "workflows", "display": "0Trust Workflows", "repo": "0trust/workflows", "desc": "Automation pipelines and cron"},
		{"name": "topology", "display": "0Trust Topology", "repo": "0trust/topology", "desc": "Fleet and network topology"},
		{"name": "nameservice", "display": "0Trust Name Service", "repo": "0trust/nameservice", "desc": "Private DNS / nameservice admin"},
		{"name": "servicekeys", "display": "0Trust Service Keys", "repo": "0trust/servicekeys", "desc": "Service API keys and fleet tokens"},
		{"name": "vpi", "display": "0Trust VPI", "repo": "0trust/vpi", "desc": "VPI product identity plane"},
		{"name": "logs", "display": "0Trust Logs", "repo": "0trust/logs", "desc": "Log explorer and observability UI"},
		{"name": "ultimate_db", "display": "Ultimate DB (kernel replication)", "repo": "0trust/ultimate-db", "desc": "Replication peer for service data — products keep local embeds and AddPeer /kernel/* (not prefer-remote)"},
		{"name": "ultimate_keystore", "display": "Ultimate Keystore (kernel replication)", "repo": "0trust/ultimate-keystore", "desc": "Replication peer for keystore kernel RPC — local material stays; peer via /kernel/keystore"},
		{"name": "tunneltug", "display": "TunnelTug engine", "repo": "tunneltug/engine", "desc": "Tunnel fleet runtime image"},
	}
}

func resolveHubProduct(name string) (hubProduct, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	p, ok := hubProductCatalog[name]
	if !ok {
		return hubProduct{}, fmt.Errorf("unknown hub product %q (want: %s; barge/engine alias tunneltug)", name, strings.Join(listHubProductNames(), ", "))
	}
	return p, nil
}

func hubImageRef(host, repo, tag string) string {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	tag = strings.TrimSpace(tag)
	if tag == "" {
		tag = "latest"
	}
	repo = strings.Trim(repo, "/")
	return host + "/" + repo + ":" + tag
}

func parseHubProductList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "all") {
		return listHubProductNames(), nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		prod, err := resolveHubProduct(p)
		if err != nil {
			return nil, err
		}
		if seen[prod.Name] {
			continue
		}
		seen[prod.Name] = true
		out = append(out, prod.Name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no products specified")
	}
	return out, nil
}
