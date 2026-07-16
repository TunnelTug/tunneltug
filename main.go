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

	if maybeGenToken() {
		return
	}

	if maybeGenBGPsecKey() {
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
	// Hub / stack modes do not use QUIC control.
	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "hub", "hub-publish", "stack":
		// skip QUIC
	default:
		ensureControlQUIC()
	}

	if err := validateConfig(); err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	switch strings.ToLower(*mode) {
	case "server":
		meshNote := ""
		if meshActive() {
			meshNote = ", mesh: on"
		}
		if *anycastEnable {
			meshNote += ", anycast: on"
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
		if *anycastEnable {
			meshNote += ", anycast: on"
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
	case "anycast":
		runAnycast()
	case "hub":
		runHub()
	case "hub-publish":
		runHubPublish()
	case "stack":
		log.Printf("Starting product stack %s [k3s self-contained, no kubectl]", Version)
		runStack()
	default:
		log.Fatalf("Unknown mode: %s. Use 'server', 'client', 'lb', 'barge', 'orchestrator', 'anycast', 'hub', 'hub-publish', or 'stack'.", *mode)
	}
}
