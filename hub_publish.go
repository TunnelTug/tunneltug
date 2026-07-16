package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// runHubPublish pushes local k3s/containerd images for catalog products to the hub.
//
//	tunneltug -mode hub-publish -hub-products mail,search,social -hub-tag dev -token $TOKEN
//
// Local images are expected as 0trust/<name>:<tag> or tunneltug/engine:<tag>
// (as created by deploy/oci/build-and-publish). Destination is hub.tunneltug.com/...
func runHubPublish() {
	if err := ensureAuthToken(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	token := strings.TrimSpace(*authToken)
	tag := strings.TrimSpace(*hubTag)
	if tag == "" {
		tag = "latest"
	}
	host := strings.TrimSpace(*hubHost)
	if host == "" {
		host = "hub.tunneltug.com"
	}

	names, err := parseHubProductList(*hubProducts)
	if err != nil {
		log.Fatalf("hub-publish: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	var failed []string
	for _, name := range names {
		prod, err := resolveHubProduct(name)
		if err != nil {
			log.Fatalf("hub-publish: %v", err)
		}
		local := prod.LocalRef + ":" + tag
		// Allow override via -k3s-hub-publish as single-product source.
		if src := strings.TrimSpace(*k3sHubPublish); src != "" && len(names) == 1 {
			local = src
		}
		dest := hubImageRef(host, prod.HubRepo, tag)
		log.Printf("hub-publish: %s → %s", local, dest)
		if err := publishK3sBargeImage(ctx, local, dest, token); err != nil {
			// Try alternate local names before failing this product.
			alts := []string{
				local,
				prod.HubRepo + ":" + tag,
				hubImageRef(host, prod.HubRepo, tag), // already local after import
				"0trust/" + prod.Name + ":" + tag,
			}
			ok := false
			var last error
			seen := map[string]bool{}
			for _, src := range alts {
				if seen[src] {
					continue
				}
				seen[src] = true
				if err := publishK3sBargeImage(ctx, src, dest, token); err != nil {
					last = err
					continue
				}
				ok = true
				log.Printf("hub-publish: ok %s (from %s)", dest, src)
				break
			}
			if !ok {
				log.Printf("hub-publish: FAILED %s: %v", name, last)
				failed = append(failed, name)
			}
			continue
		}
		log.Printf("hub-publish: ok %s", dest)
	}

	if len(failed) > 0 {
		log.Fatalf("hub-publish incomplete; failed: %s", strings.Join(failed, ", "))
	}
	fmt.Printf("Published %d product(s) to %s tag=%s\n", len(names), host, tag)
}
