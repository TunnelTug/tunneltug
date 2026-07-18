package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// runHubPublish pushes catalog products to the TunnelTug hub.
//
//	tunneltug -mode hub-publish -hub-products name,dbsc_relay,anycast -hub-tag latest -token $TOKEN
//
// Preferred path: local k3s/containerd image → k3s ctr push (barge layer).
// Fallback: pack a linux binary from -hub-dist (or default dist paths) and push
// over the hub Registry API. No external image tools required.
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

		if err := publishK3sBargeImage(ctx, local, dest, token); err == nil {
			log.Printf("hub-publish: ok %s (k3s ctr)", dest)
			continue
		} else {
			// Try alternate local k3s names.
			alts := []string{
				local,
				prod.HubRepo + ":" + tag,
				hubImageRef(host, prod.HubRepo, tag),
				"0trust/" + prod.Name + ":" + tag,
			}
			ok := false
			var last error = err
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
				log.Printf("hub-publish: ok %s (k3s from %s)", dest, src)
				break
			}
			if ok {
				continue
			}
			// Built-in binary pack + Registry API push (TunnelTug hub, no crane/docker).
			binPath, berr := resolveHubBinary(prod.Name)
			if berr != nil {
				log.Printf("hub-publish: FAILED %s: k3s=%v; binary=%v", name, last, berr)
				failed = append(failed, name)
				continue
			}
			log.Printf("hub-publish: packing binary %s → %s", binPath, dest)
			if err := publishBinaryToHub(binPath, host, prod.HubRepo, tag, token); err != nil {
				log.Printf("hub-publish: FAILED %s: %v", name, err)
				failed = append(failed, name)
				continue
			}
			log.Printf("hub-publish: ok %s (binary pack)", dest)
		}
	}

	if len(failed) > 0 {
		log.Fatalf("hub-publish incomplete; failed: %s", strings.Join(failed, ", "))
	}
	fmt.Printf("Published %d product(s) to %s tag=%s\n", len(names), host, tag)
}
