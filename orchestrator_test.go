package main

import (
	"testing"
)

func resetOrchestratorFlags(t *testing.T) {
	t.Helper()
	resetLBFlags(t)
	*mode = "orchestrator"
	*orchDashPort = "4060"
}

func TestValidateConfig_AcceptsOrchestratorMode(t *testing.T) {
	resetOrchestratorFlags(t)
	*lbBackends = ""
	*lbDynamic = true
	if err := validateConfig(); err != nil {
		t.Fatalf("expected valid orchestrator config, got %v", err)
	}
}

func TestLBManager_NamespaceBackendFilter(t *testing.T) {
	m := newLBManager([]*tunnelBackend{
		{id: "a:1", namespace: "prod"},
		{id: "b:1", namespace: "staging"},
	})
	filtered := m.backendsForNamespace("prod")
	if len(filtered) != 1 || filtered[0].id != "a:1" {
		t.Fatalf("unexpected filtered backends: %+v", filtered)
	}
}

func TestLBManager_NamespaceSummary(t *testing.T) {
	m := newLBManager([]*tunnelBackend{
		{id: "a:1", namespace: "prod"},
		{id: "b:1", namespace: "staging"},
	})
	m.registerRoute("prod/myapp", m.backends[0])
	summary := m.namespaceSummary()
	if len(summary) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(summary))
	}
}