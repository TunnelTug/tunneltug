package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
)

func registerLBAddr() string {
	return strings.TrimSpace(*registerLB)
}

func serverSelfRegisterActive() bool {
	return registerLBAddr() != ""
}

func resolveRegisterEndpoint() (lbEndpoint, error) {
	host := strings.TrimSpace(*registerHost)
	if host == "" {
		host = strings.TrimSpace(*bargeHost)
	}
	if host == "" {
		return lbEndpoint{}, fmt.Errorf("-register-host is required with -register-lb (use node IP or -barge-host)")
	}

	fleet := strings.TrimSpace(*registerFleetID)
	if fleet == "" {
		if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
			fleet = strings.TrimSpace(h)
		} else {
			fleet = "server"
		}
	}

	return lbEndpoint{
		Host:        host,
		ControlPort: strings.TrimSpace(*controlPort),
		PublicPort:  strings.TrimSpace(*publicPort),
		Namespace:   normalizeNamespace(*namespace),
		FleetID:     fleet,
	}, nil
}

// startServerLBRegistration registers this server process with an LB until ctx is cancelled.
func startServerLBRegistration(ctx context.Context) {
	if !serverSelfRegisterActive() {
		return
	}

	ep, err := resolveRegisterEndpoint()
	if err != nil {
		log.Fatalf("Server LB registration config: %v", err)
	}
	reg, err := newBargeLBRegistrar(registerLBAddr())
	if err != nil {
		log.Fatalf("Server LB registration config: %v", err)
	}

	label := fmt.Sprintf("Server %s", ep.FleetID)
	go reg.runRegistrationLifecycle(ctx, ep, label)
}
