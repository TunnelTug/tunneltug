package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// hostnameOrdinal parses a StatefulSet-style hostname ending in "-N" (e.g. tunneltug-barge-2 → 2).
func hostnameOrdinal(hostname string) (int, error) {
	host := strings.TrimSpace(hostname)
	if host == "" {
		return 0, fmt.Errorf("empty hostname")
	}
	// Strip domain if FQDN.
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	idx := strings.LastIndexByte(host, '-')
	if idx < 0 || idx == len(host)-1 {
		return 0, fmt.Errorf("hostname %q has no trailing -N ordinal", hostname)
	}
	n, err := strconv.Atoi(host[idx+1:])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("hostname %q has invalid ordinal suffix", hostname)
	}
	return n, nil
}

// portForIndex returns base + index*step as a port string.
func portForIndex(base string, index, step int) (string, error) {
	start, err := strconv.Atoi(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("invalid base port %q", base)
	}
	if step < 1 {
		return "", fmt.Errorf("port step must be at least 1")
	}
	if index < 0 {
		return "", fmt.Errorf("index must be non-negative")
	}
	port := start + index*step
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("computed port %d out of range (base %d, index %d, step %d)", port, start, index, step)
	}
	return strconv.Itoa(port), nil
}

// applyIndexFromHostname adjusts -control/-public/-dash from hostname ordinal when enabled.
func applyIndexFromHostname() error {
	if !*indexFromHostname {
		return nil
	}
	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("index-from-hostname: resolve hostname: %w", err)
	}
	index, err := hostnameOrdinal(host)
	if err != nil {
		return fmt.Errorf("index-from-hostname: %w", err)
	}
	step := *bargePortStep
	if step < 1 {
		step = 1
	}

	control, err := portForIndex(*controlPort, index, step)
	if err != nil {
		return fmt.Errorf("index-from-hostname control: %w", err)
	}
	public, err := portForIndex(*publicPort, index, step)
	if err != nil {
		return fmt.Errorf("index-from-hostname public: %w", err)
	}
	dash, err := portForIndex(*dashPort, index, step)
	if err != nil {
		return fmt.Errorf("index-from-hostname dash: %w", err)
	}

	*controlPort = control
	*publicPort = public
	*dashPort = dash
	return nil
}
