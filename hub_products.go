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

type hubProduct struct {
	Name     string // mail, search, platform, services, social, tunneltug
	HubRepo  string // 0trust/mail or tunneltug/engine
	LocalRef string // default local k3s image name before push
}

var hubProductCatalog = map[string]hubProduct{
	"mail": {
		Name: "mail", HubRepo: "0trust/mail", LocalRef: "0trust/mail",
	},
	"search": {
		Name: "search", HubRepo: "0trust/search", LocalRef: "0trust/search",
	},
	"platform": {
		Name: "platform", HubRepo: "0trust/platform", LocalRef: "0trust/platform",
	},
	"services": {
		Name: "services", HubRepo: "0trust/services", LocalRef: "0trust/services",
	},
	"social": {
		Name: "social", HubRepo: "0trust/social", LocalRef: "0trust/social",
	},
	"cdn": { // alias for social / 0trust.social CDN
		Name: "social", HubRepo: "0trust/social", LocalRef: "0trust/social",
	},
	// Ack — generalized DEF CON / event chat product (defcon.chat).
	"ack": {
		Name: "ack", HubRepo: "0trust/ack", LocalRef: "0trust/ack",
	},
	"defcon": {
		Name: "ack", HubRepo: "0trust/ack", LocalRef: "0trust/ack",
	},
	// MotionKB — docs / CMS / published sites.
	"motionkb": {
		Name: "motionkb", HubRepo: "0trust/motionkb", LocalRef: "0trust/motionkb",
	},
	"motion": {
		Name: "motionkb", HubRepo: "0trust/motionkb", LocalRef: "0trust/motionkb",
	},
	// Williwaw — social feed / stories.
	"williwaw": {
		Name: "williwaw", HubRepo: "0trust/williwaw", LocalRef: "0trust/williwaw",
	},
	// TunnelTug engine — image used by barge-mode k3s pods (not a separate "barge product").
	"tunneltug": {
		Name: "tunneltug", HubRepo: "tunneltug/engine", LocalRef: "tunneltug/engine",
	},
	"engine": {
		Name: "tunneltug", HubRepo: "tunneltug/engine", LocalRef: "tunneltug/engine",
	},
	// Legacy alias: people said "barge image" meaning the engine image for barge fleets.
	"barge": {
		Name: "tunneltug", HubRepo: "tunneltug/engine", LocalRef: "tunneltug/engine",
	},
}

func listHubProductNames() []string {
	return []string{"mail", "search", "platform", "services", "social", "ack", "motionkb", "williwaw", "tunneltug"}
}

// HubProductCatalogPublic is a UI-friendly list for tunneltug.com hub page.
func HubProductCatalogPublic() []map[string]string {
	return []map[string]string{
		{"name": "ack", "display": "Ack", "repo": "0trust/ack", "desc": "Event chat (DEF CON / generalized platform)"},
		{"name": "williwaw", "display": "Williwaw", "repo": "0trust/williwaw", "desc": "Social feed and stories"},
		{"name": "motionkb", "display": "MotionKB", "repo": "0trust/motionkb", "desc": "Docs CMS and published sites"},
		{"name": "mail", "display": "MeshMail", "repo": "0trust/mail", "desc": "Encrypted mesh mail"},
		{"name": "search", "display": "MeshSearch", "repo": "0trust/search", "desc": "Private search index"},
		{"name": "platform", "display": "0Trust Platform", "repo": "0trust/platform", "desc": "Identity + control plane"},
		{"name": "services", "display": "0Trust Services", "repo": "0trust/services", "desc": "Mesh edge / ZTNA agents"},
		{"name": "social", "display": "0Trust CDN", "repo": "0trust/social", "desc": "0trust.social S3 + media CDN"},
		{"name": "tunneltug", "display": "TunnelTug engine", "repo": "tunneltug/engine", "desc": "Binary inside k3s fleet pods (barge mode)"},
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
