package main

import (
	"os"
	"strings"
	"testing"

	"github.com/0TrustCloud/guikit"
)

func TestParseStackProducts_Default(t *testing.T) {
	apps, err := parseStackProducts("")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, a := range apps {
		names[a.Name] = true
	}
	for _, want := range []string{"williwaw", "motionkb", "ack", "social", "orchid-ingest"} {
		if !names[want] {
			t.Fatalf("default stack missing %s: %v", want, names)
		}
	}
}

func TestParseStackProducts_WilliwawMotion(t *testing.T) {
	apps, err := parseStackProducts("williwaw,motionkb,motion")
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 {
		t.Fatalf("got %d apps (motion alias should collapse)", len(apps))
	}
}

func TestParseStackProducts_SkipsEngine(t *testing.T) {
	apps, err := parseStackProducts("williwaw,tunneltug,barge")
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Name != "williwaw" {
		t.Fatalf("engine should be skipped: %+v", apps)
	}
}

func TestStackImage(t *testing.T) {
	prevHost, prevTag := *hubHost, *hubTag
	t.Cleanup(func() { *hubHost = prevHost; *hubTag = prevTag })
	*hubHost = "hub.tunneltug.com"
	*hubTag = "dev"
	app := stackCatalog()["williwaw"]
	got := stackImage(app, "")
	if got != "hub.tunneltug.com/0trust/williwaw:dev" {
		t.Fatalf("got %s", got)
	}
	if !strings.Contains(stackImage(app, "1.0.0"), ":1.0.0") {
		t.Fatal("tag override")
	}
}

func TestStackShouldHostPort_Policy(t *testing.T) {
	prev := *stackHostPorts
	t.Cleanup(func() { *stackHostPorts = prev })

	plat := stackCatalog()["platform"]
	mail := stackCatalog()["mail"]

	*stackHostPorts = "products"
	if stackShouldHostPort(plat) {
		t.Fatal("platform should not hostPort under products")
	}
	if !stackShouldHostPort(mail) {
		t.Fatal("mail should hostPort under products")
	}

	*stackHostPorts = "all"
	if !stackShouldHostPort(plat) || !stackShouldHostPort(mail) {
		t.Fatal("all should hostPort everything")
	}

	*stackHostPorts = "none"
	if stackShouldHostPort(plat) || stackShouldHostPort(mail) {
		t.Fatal("none should disable hostPort")
	}

	on := true
	plat.HostPort = &on
	*stackHostPorts = "none"
	if !stackShouldHostPort(plat) {
		t.Fatal("per-barge host_port: true should win")
	}
	off := false
	mail.HostPort = &off
	*stackHostPorts = "all"
	if stackShouldHostPort(mail) {
		t.Fatal("per-barge host_port: false should win")
	}
}

func TestStackLocalAccessURL(t *testing.T) {
	prevHP, prevDash, prevGW := *stackHostPorts, *stackDashPort, *stackGateway
	t.Cleanup(func() {
		*stackHostPorts = prevHP
		*stackDashPort = prevDash
		*stackGateway = prevGW
	})
	*stackDashPort = "4070"
	*stackHostPorts = "products"
	*stackGateway = true

	// One-port model: gateway is primary even when hostPort would apply.
	mail := stackCatalog()["mail"]
	if u := stackLocalAccessURL(mail); u != "http://127.0.0.1:4070/apps/mail/" {
		t.Fatalf("mail access %s", u)
	}
	plat := stackCatalog()["platform"]
	if u := stackLocalAccessURL(plat); u != "http://127.0.0.1:4070/apps/platform/" {
		t.Fatalf("platform gateway access %s", u)
	}
}

func TestStackMeshFQDN(t *testing.T) {
	prevZ := *meshZone
	t.Cleanup(func() { *meshZone = prevZ })
	*meshZone = "tunneltug.tunnel"
	mail := stackCatalog()["mail"]
	if got := stackMeshFQDN(mail); got != "mail.tunneltug.tunnel" {
		t.Fatalf("mesh fqdn %s", got)
	}
	if got := stackMeshURL(mail); !strings.HasPrefix(got, "https://mail.tunneltug.tunnel:") {
		t.Fatalf("mesh url %s", got)
	}
}

func TestStackDashboardGML(t *testing.T) {
	prevGW, prevHP, prevDash, prevZone := *stackGateway, *stackHostPorts, *stackDashPort, *meshZone
	t.Cleanup(func() {
		*stackGateway = prevGW
		*stackHostPorts = prevHP
		*stackDashPort = prevDash
		*meshZone = prevZone
	})
	*stackGateway = true
	*stackHostPorts = "all"
	*stackDashPort = "4070"
	*meshZone = "tunneltug.tunnel"
	st := &stackStatus{
		ns:  "0trust-stack",
		tag: "latest",
		apps: []map[string]any{
			{
				"name": "platform", "image": "hub.tunneltug.com/0trust/platform:latest",
				"phase": "Ready", "ready": 1, "desired": 1, "port": int32(8443),
				"access_url": "http://127.0.0.1:4070/apps/platform/", "gateway": "/apps/platform/",
				"mesh_fqdn": "platform.tunneltug.tunnel",
				"mesh_url":  "https://platform.tunneltug.tunnel:8443/",
				"0trustos":  true,
			},
		},
	}
	data := stackDashboardData(st)
	html, err := guikit.RenderGML(stackDashboardGML, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"TunnelTug product stack",
		"0trust-stack",
		"platform",
		"/apps/platform/",
		"platform.tunneltug.tunnel",
		// meshTLS HTTPS open (or plain gateway path still present)
		"platform.tunneltug.tunnel",
		"Mesh address",
		"tt-shell",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("GML render missing %q in:\n%s", want, html[:min(800, len(html))])
		}
	}
}

func TestRewriteStackGatewayLocation(t *testing.T) {
	prefix := "/apps/platform"
	cases := []struct {
		in, want string
	}{
		{"/welcome", "/apps/platform/welcome"},
		{"/apps/platform/welcome", "/apps/platform/welcome"},
		{"http://127.0.0.1:8443/auth", "/apps/platform/auth"},
		{"https://other.example/x", "https://other.example/x"},
		{"", ""},
	}
	for _, tc := range cases {
		got := rewriteStackGatewayLocation(prefix, "127.0.0.1:8443", tc.in)
		if got != tc.want {
			t.Fatalf("rewrite %q: got %q want %q", tc.in, got, tc.want)
		}
	}
	// Host-based mesh: no rewrite.
	if got := rewriteStackGatewayLocation("", "127.0.0.1:8443", "/welcome"); got != "/welcome" {
		t.Fatalf("host mesh rewrite = %q", got)
	}
}

func TestAdaptStackConfigContent_FacePort(t *testing.T) {
	raw := []byte(`
server:
  host: platform.mitigation.local
  domain: http://platform.mitigation.local:8443
  port: "8443"
  quic_port: "8443"
  http_only: true
platform:
  name: lab
`)
	auth := stackCatalog()["auth"]
	auth.Domain = "auth.mitigation.local"
	auth.PublicURL = "http://auth.mitigation.local:8460"
	out := adaptStackConfigContent(raw, auth)
	body := string(out)
	for _, want := range []string{
		`port: "8460"`,
		`quic_port: "8460"`,
		`host: auth.mitigation.local`,
		`domain: http://auth.mitigation.local:8460`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in adapted config:\n%s", want, body)
		}
	}
	if strings.Contains(body, `port: "8443"`) {
		t.Fatalf("still has 8443:\n%s", body)
	}
}

func TestLoadStackConfig_HostPortsAndHostPort(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/stack.yaml"
	body := `
namespace: mit-stack
host_ports: all
barges:
  - name: platform
    host_port: true
  - name: services
    host_port: false
    args: ["--config", "/config/services.yaml"]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := *stackHostPorts
	t.Cleanup(func() { *stackHostPorts = prev })
	got, err := loadStackConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.HostPorts != "all" {
		t.Fatalf("host_ports %q", got.HostPorts)
	}
	if got.Namespace != "mit-stack" {
		t.Fatalf("ns %s", got.Namespace)
	}
	var plat, svc *stackApp
	for i := range got.Apps {
		switch got.Apps[i].Name {
		case "platform":
			plat = &got.Apps[i]
		case "services":
			svc = &got.Apps[i]
		}
	}
	if plat == nil || plat.HostPort == nil || !*plat.HostPort {
		t.Fatalf("platform host_port: %+v", plat)
	}
	if svc == nil || svc.HostPort == nil || *svc.HostPort {
		t.Fatalf("services host_port false: %+v", svc)
	}
	if len(svc.Args) != 2 || svc.Args[0] != "--config" {
		t.Fatalf("services args: %+v", svc.Args)
	}
}

func TestStackAppUses0TrustOS(t *testing.T) {
	if !stackAppUses0TrustOS(stackApp{Name: "access"}) {
		t.Fatal("access should use 0TrustOS")
	}
	if !stackAppUses0TrustOS(stackApp{Name: "platform"}) {
		t.Fatal("platform should use 0TrustOS")
	}
	if stackAppUses0TrustOS(stackApp{Name: "ultimate-db"}) {
		t.Fatal("ultimate-db should not use 0TrustOS product entry")
	}
	if stackAppUses0TrustOS(stackApp{Name: "anycast"}) {
		t.Fatal("anycast should not use 0TrustOS product entry")
	}
}
