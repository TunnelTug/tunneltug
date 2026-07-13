package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"strings"
)

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Println(versionString())
		return
	}

	if *quiet {
		log.SetOutput(io.Discard)
	}

	applyEnvDefaults()
	applyProductionDefaults()
	if err := applyIndexFromHostname(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	if strings.ToLower(strings.TrimSpace(*mode)) == "barge" {
		bargeScalingProfile()
	}
	ensureControlQUIC()

	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	switch strings.ToLower(*mode) {
	case "server":
		meshNote := ""
		if meshActive() {
			meshNote = ", mesh: on"
		}
		log.Printf("Starting server %s [control: QUIC, routing: %s%s]", Version, *routing, meshNote)
		runServer()
	case "client":
		meshNote := ""
		if meshActive() {
			meshNote = ", mesh: on"
			if vpiActive() {
				meshNote += ", vpi: on"
			}
		}
		log.Printf("Starting client %s [control: QUIC, routing: %s%s]", Version, *routing, meshNote)
		runClient()
	case "lb":
		meshNote := ""
		if meshActive() {
			meshNote = ", mesh: on"
		}
		log.Printf("Starting load balancer %s [control: QUIC, routing: %s, policy: %s%s]", Version, *routing, *lbPolicy, meshNote)
		runLB()
	case "barge":
		log.Printf("Starting barge fleet %s [runtime: %s, service: %s, replicas: %d, namespace: %s]", Version, bargeRuntimeMode(), *bargeService, *bargeReplicas, normalizeNamespace(*namespace))
		runBarge()
	case "orchestrator":
		meshNote := ""
		if meshActive() {
			meshNote = ", mesh: on"
		}
		log.Printf("Starting orchestrator %s [routing: %s, policy: %s, namespace: %s%s]", Version, *routing, *lbPolicy, normalizeNamespace(*namespace), meshNote)
		runOrchestrator()
	default:
		log.Fatalf("Unknown mode: %s. Use 'server', 'client', 'lb', 'barge', or 'orchestrator'.", *mode)
	}
}
