package main

import (
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/remotecommand"
)

func TestSanitizeShellApp(t *testing.T) {
	cases := map[string]string{
		"access":                "access",
		"Ultimate_DB":           "ultimate-db",
		"orchid-ingest":         "orchid-ingest",
		"../etc":                "",
		"app;rm":                "",
		"":                      "",
		strings.Repeat("a", 64): "",
	}
	for in, want := range cases {
		if got := sanitizeShellApp(in); got != want {
			t.Fatalf("sanitizeShellApp(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSanitizeShellCmd(t *testing.T) {
	def := resolveShellCmd("")
	if len(def) < 2 || def[0] != "/usr/bin/gonode" || def[1] != "-cli" {
		t.Fatalf("default should be gonode Junos CLI, got %v", def)
	}
	for _, alias := range []string{"gonode", "cli", "junos"} {
		got := resolveShellCmd(alias)
		if got[0] != "/usr/bin/gonode" || got[1] != "-cli" {
			t.Fatalf("alias %q: %v", alias, got)
		}
	}
	if got := resolveShellCmd("sh"); len(got) != 1 || got[0] != "/bin/sh" {
		t.Fatalf("sh escape: %v", got)
	}
	if got := resolveShellCmd("/bin/bash"); len(got) != 1 || got[0] != "/bin/bash" {
		t.Fatalf("bash path: %v", got)
	}
	// Injection attempts fall back to gonode CLI (safe default).
	evil := resolveShellCmd("/bin/sh -c evil")
	if evil[0] != "/usr/bin/gonode" {
		t.Fatalf("evil cmd not sanitized to gonode: %v", evil)
	}
}

func TestTermSizeQueue(t *testing.T) {
	q := newTermSizeQueue()
	q.Push(120, 40)
	sz := q.Next()
	if sz == nil || sz.Width != 120 || sz.Height != 40 {
		t.Fatalf("size: %+v", sz)
	}
	q.Close()
	if q.Next() != nil {
		t.Fatal("expected nil after close")
	}
	_ = remotecommand.TerminalSize{}
}

func TestStackShellHTML_0TrustOS(t *testing.T) {
	html := stackShellHTML("access", "0trust-stack", "")
	for _, want := range []string{"0TrustOS", "access", "0trust-stack", "/shell/ws", "xterm", "gonode", "Junos"} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q", want)
		}
	}
	if !strings.Contains(html, `"access"`) {
		t.Fatal("missing JSON app string")
	}
}

func TestIs0TrustOSContainer(t *testing.T) {
	ok := is0TrustOSContainer(corev1.Container{
		Name:  "platform",
		Image: "hub.tunneltug.com/0trust/platform:latest",
		Env: []corev1.EnvVar{
			{Name: "OTRUST_PRODUCT", Value: "platform"},
			{Name: "GONODE_DATA", Value: "/var/lib/gonode"},
		},
	})
	if !ok {
		t.Fatal("expected 0TrustOS product container")
	}
	// Engine image is not a product 0TrustOS face for shell.
	engine := is0TrustOSContainer(corev1.Container{
		Name:  "engine",
		Image: "hub.tunneltug.com/tunneltug/engine:latest",
	})
	if engine {
		t.Fatal("engine should not count as 0TrustOS product shell target")
	}
	// Env-only (image already rewritten by digest) still counts.
	envOnly := is0TrustOSContainer(corev1.Container{
		Name:  "williwaw",
		Image: "sha256:deadbeef",
		Env:   []corev1.EnvVar{{Name: "PRODUCT_BIN", Value: "/usr/local/bin/product"}},
	})
	if !envOnly {
		t.Fatal("PRODUCT_BIN should mark 0TrustOS")
	}
}

func TestParseShellSize(t *testing.T) {
	r, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/shell/ws?cols=100&rows=30", nil)
	if err != nil {
		t.Fatal(err)
	}
	c, rows := parseShellSize(r)
	if c != 100 || rows != 30 {
		t.Fatalf("got %d x %d", c, rows)
	}
}
