package main

import (
	"fmt"
	"regexp"
	"strings"
)

const defaultNamespace = "default"

var namespacePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func normalizeNamespace(value string) string {
	ns := strings.ToLower(strings.TrimSpace(value))
	if ns == "" {
		return defaultNamespace
	}
	return ns
}

func validateNamespace(value string) error {
	ns := strings.TrimSpace(value)
	if ns == "" {
		return nil
	}
	if !namespacePattern.MatchString(strings.ToLower(ns)) {
		return fmt.Errorf("invalid -namespace %q: use lowercase letters, numbers, and hyphens", value)
	}
	return nil
}

func composeTunnelKey(namespace, subdomain string) string {
	ns := normalizeNamespace(namespace)
	sub := strings.ToLower(strings.TrimSpace(subdomain))
	if isDirectRouting() {
		sub = defaultTunnelKey
	}
	if ns == defaultNamespace {
		return sub
	}
	return ns + "/" + sub
}

func splitTunnelKey(key string) (namespace, subdomain string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return defaultNamespace, ""
	}
	if strings.Contains(key, "/") {
		parts := strings.SplitN(key, "/", 2)
		return normalizeNamespace(parts[0]), parts[1]
	}
	return defaultNamespace, key
}

func tunnelKeyFromHost(host string) string {
	if isDirectRouting() {
		return composeTunnelKey(*namespace, defaultTunnelKey)
	}

	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return ""
	}

	baseDomain := strings.TrimSpace(*domain)
	if baseDomain != "" {
		suffix := "." + baseDomain
		if strings.HasSuffix(host, suffix) {
			prefix := strings.TrimSuffix(host, suffix)
			if prefix == "" {
				return ""
			}
			parts := strings.Split(prefix, ".")
			switch len(parts) {
			case 1:
				return composeTunnelKey(defaultNamespace, parts[0])
			case 2:
				return composeTunnelKey(parts[1], parts[0])
			default:
				return composeTunnelKey(parts[len(parts)-1], parts[0])
			}
		}
		if host == baseDomain {
			return ""
		}
	}

	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	sub := parts[0]
	if sub == "localhost" || sub == "127" || sub == "0" {
		return ""
	}
	if len(parts) >= 3 {
		return composeTunnelKey(parts[1], sub)
	}
	return composeTunnelKey(defaultNamespace, sub)
}

func clientTunnelKey() string {
	return composeTunnelKey(*namespace, *subdomain)
}

func publicURL() string {
	host := *serverIP
	if *domain != "" {
		host = *domain
	} else if host == "" || host == "127.0.0.1" {
		host = "localhost"
	}

	scheme := publicScheme()
	defaultPort := "80"
	if scheme == "https" {
		defaultPort = "443"
	}

	if isDirectRouting() {
		if *publicPort == defaultPort {
			return fmt.Sprintf("%s://%s", scheme, host)
		}
		return fmt.Sprintf("%s://%s:%s", scheme, host, *publicPort)
	}

	ns := normalizeNamespace(*namespace)
	subHost := fmt.Sprintf("%s.%s", *subdomain, host)
	if ns != defaultNamespace {
		subHost = fmt.Sprintf("%s.%s.%s", *subdomain, ns, host)
	}
	if *publicPort == defaultPort {
		return fmt.Sprintf("%s://%s", scheme, subHost)
	}
	return fmt.Sprintf("%s://%s:%s", scheme, subHost, *publicPort)
}

func namespaceRouteHint() string {
	base := strings.TrimSpace(*domain)
	if base == "" {
		base = "localhost"
	}
	ns := normalizeNamespace(*namespace)
	if ns == defaultNamespace {
		return fmt.Sprintf("myapp.%s", base)
	}
	return fmt.Sprintf("myapp.%s.%s", ns, base)
}