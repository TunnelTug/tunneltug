package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStackConfig_BargesAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	body := `
namespace: test-stack
tag: v1
hub_host: hub.example.com
barges:
  - name: williwaw
    replicas: 3
    env:
      FOO: bar
  - name: social
    replicas: 1
  - name: tunneltug
    # engine skipped
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Defaults for global flags used by resolve.
	prevTag, prevHub, prevHost := *stackTag, *hubTag, *hubHost
	t.Cleanup(func() { *stackTag = prevTag; *hubTag = prevHub; *hubHost = prevHost })
	*stackTag = ""
	*hubTag = "latest"
	*hubHost = "hub.tunneltug.com"

	got, err := loadStackConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Namespace != "test-stack" || got.Tag != "v1" || got.HubHost != "hub.example.com" {
		t.Fatalf("meta: %+v", got)
	}
	if len(got.Apps) != 2 {
		t.Fatalf("want 2 apps (engine skipped), got %d", len(got.Apps))
	}
	var ww *stackApp
	for i := range got.Apps {
		if got.Apps[i].Name == "williwaw" {
			ww = &got.Apps[i]
		}
	}
	if ww == nil {
		t.Fatal("williwaw missing")
	}
	if ww.Replicas != 3 {
		t.Fatalf("replicas=%d", ww.Replicas)
	}
	if ww.Env["FOO"] != "bar" {
		t.Fatalf("env merge: %+v", ww.Env)
	}
	if ww.Env["WILLIWAW_DB"] == "" {
		t.Fatal("catalog env should remain")
	}
	if ww.TagOverride != "v1" {
		t.Fatalf("tag override %q", ww.TagOverride)
	}
	img := stackImage(*ww, got.Tag)
	if img != "hub.example.com/0trust/williwaw:v1" {
		t.Fatalf("image %s", img)
	}
}

func TestLoadStackConfig_ProductsKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(path, []byte(`
products:
  - name: auth
  - name: idp
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadStackConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// idp aliases to auth — one app.
	if len(got.Apps) != 1 || got.Apps[0].Name != "auth" {
		t.Fatalf("got %+v", got.Apps)
	}
}

func TestLoadStackConfig_ConfigFileAndFileInclude(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen: :1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bargePath := filepath.Join(dir, "williwaw.yaml")
	if err := os.WriteFile(bargePath, []byte(`
name: williwaw
replicas: 2
config_file: app.yaml
config_mount: /etc/williwaw
config_key: williwaw.yaml
env:
  FROM_FILE: "1"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	stackPath := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(stackPath, []byte(`
barges:
  - name: williwaw
    file: williwaw.yaml
    env:
      FROM_STACK: "1"
  - name: anycast
    config_file: app.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadStackConfig(stackPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Apps) != 2 {
		t.Fatalf("apps=%d", len(got.Apps))
	}
	var ww, ac *stackApp
	for i := range got.Apps {
		switch got.Apps[i].Name {
		case "williwaw":
			ww = &got.Apps[i]
		case "anycast":
			ac = &got.Apps[i]
		}
	}
	if ww == nil || ac == nil {
		t.Fatalf("missing apps: %+v", got.Apps)
	}
	if ww.Replicas != 2 {
		t.Fatalf("replicas from file: %d", ww.Replicas)
	}
	if ww.Env["FROM_FILE"] != "1" || ww.Env["FROM_STACK"] != "1" {
		t.Fatalf("env merge: %+v", ww.Env)
	}
	if ww.ConfigFile != cfgPath {
		t.Fatalf("config path want %s got %s", cfgPath, ww.ConfigFile)
	}
	if ww.ConfigMount != "/etc/williwaw" || ww.ConfigKey != "williwaw.yaml" {
		t.Fatalf("mount/key: %s %s", ww.ConfigMount, ww.ConfigKey)
	}
	if ac.ConfigFile != cfgPath {
		t.Fatalf("anycast config: %s", ac.ConfigFile)
	}
	if ac.ConfigMount != "/config" {
		t.Fatalf("default mount: %s", ac.ConfigMount)
	}
}

func TestLoadStackConfig_DisabledAndUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(path, []byte(`
barges:
  - name: williwaw
    disabled: true
  - name: social
`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadStackConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Apps) != 1 || got.Apps[0].Name != "social" {
		t.Fatalf("disabled should skip: %+v", got.Apps)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte(`barges: [{name: not-a-product}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStackConfig(bad); err == nil {
		t.Fatal("expected unknown product error")
	}
}

func TestLoadStackConfig_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte(`namespace: x`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStackConfig(path); err == nil {
		t.Fatal("expected empty products error")
	}
}

func TestStackConfigPath_Alias(t *testing.T) {
	prevS, prevB := *stackConfig, *bargeConfig
	t.Cleanup(func() { *stackConfig = prevS; *bargeConfig = prevB })

	*stackConfig = ""
	*bargeConfig = "/tmp/barge.yaml"
	if stackConfigPath() != "/tmp/barge.yaml" {
		t.Fatalf("barge-config alias: %q", stackConfigPath())
	}
	*stackConfig = "/tmp/stack.yaml"
	if stackConfigPath() != "/tmp/stack.yaml" {
		t.Fatalf("stack-config wins: %q", stackConfigPath())
	}
}

func TestStackImage_Overrides(t *testing.T) {
	prevHost, prevTag := *hubHost, *hubTag
	t.Cleanup(func() { *hubHost = prevHost; *hubTag = prevTag })
	*hubHost = "hub.tunneltug.com"
	*hubTag = "latest"

	app := stackCatalog()["williwaw"]
	app.ImageOverride = "registry.local/williwaw:custom"
	if stackImage(app, "ignored") != "registry.local/williwaw:custom" {
		t.Fatal(stackImage(app, "ignored"))
	}
	app.ImageOverride = ""
	app.TagOverride = "canary"
	app.HubHostOverride = "hub.internal"
	if stackImage(app, "default") != "hub.internal/0trust/williwaw:canary" {
		t.Fatal(stackImage(app, "default"))
	}
}

func TestApplyBargeOverrides_PortAndReplicas(t *testing.T) {
	base := stackCatalog()["auth"]
	port := int32(9001)
	reps := int32(4)
	cfg := BargeProductConfig{
		Name:     "auth",
		Port:     &port,
		Replicas: &reps,
		Env:      map[string]string{"EXTRA": "1"},
	}
	app := applyBargeOverrides(base, cfg, "dev", "hub.tunneltug.com", "", "")
	if app.Port != 9001 || app.Replicas != 4 {
		t.Fatalf("%+v", app)
	}
	if app.Env["TRUST_PRODUCT"] != "auth" || app.Env["EXTRA"] != "1" {
		t.Fatalf("env: %+v", app.Env)
	}
	if app.Env["TRUST_PORT"] != "9001" {
		t.Fatalf("YAML port should sync TRUST_PORT: %+v", app.Env)
	}
	// Catalog must not be mutated.
	if stackCatalog()["auth"].Port != 8460 {
		t.Fatal("catalog mutated")
	}
}

func TestSyncStackListenEnv_WilliwawYAMLPort(t *testing.T) {
	base := stackCatalog()["williwaw"]
	port := int32(13081)
	app := applyBargeOverrides(base, BargeProductConfig{Name: "williwaw", Port: &port}, "latest", "hub.tunneltug.com", "", "")
	if app.Env["WILLIWAW_LISTEN"] != ":13081" {
		t.Fatalf("listen env: %+v", app.Env)
	}
	all := []stackApp{app}
	env := productLinkEnv(app, all, "ns")
	if env["WILLIWAW_PUBLIC_URL"] != "http://williwaw.ns.svc:13081" {
		t.Fatalf("public url: %+v", env)
	}
}

func TestResolveBargePublicURL_YAMLDomain(t *testing.T) {
	app := stackApp{Name: "williwaw", Port: 3081, Domain: "feed.example.com", PublicScheme: "https"}
	if got := resolveBargePublicURL(app, "ns"); got != "https://feed.example.com" {
		t.Fatalf("domain: %s", got)
	}
	app = stackApp{Name: "williwaw", Port: 3081, PublicURL: "https://custom.example/"}
	if got := resolveBargePublicURL(app, "ns"); got != "https://custom.example" {
		t.Fatalf("public_url: %s", got)
	}
	app = stackApp{Name: "williwaw", Port: 3081, StackDomain: "example.com", PublicScheme: "https"}
	if got := resolveBargePublicURL(app, "ns"); got != "https://williwaw.example.com" {
		t.Fatalf("stack domain product: %s", got)
	}
	app = stackApp{Name: "auth", Port: 8460, StackDomain: "example.com", PublicScheme: "https"}
	if got := resolveBargePublicURL(app, "ns"); got != "https://example.com" {
		t.Fatalf("stack domain apex: %s", got)
	}
	// No domain → in-cluster only (never a hardcoded public TLD).
	app = stackApp{Name: "williwaw", Port: 3081}
	if got := resolveBargePublicURL(app, "demo"); got != "http://williwaw.demo.svc:3081" {
		t.Fatalf("cluster: %s", got)
	}
}

func TestProductLinkEnv_SiblingPortsFromStack(t *testing.T) {
	social := stackApp{Name: "social", Port: 13085}
	ack := stackApp{Name: "ack", Port: 13083}
	ww := stackApp{Name: "williwaw", Port: 13081, Domain: "ww.example.com", PublicScheme: "https"}
	all := []stackApp{social, ack, ww}
	env := productLinkEnv(ww, all, "stack")
	if env["WILLIWAW_PUBLIC_URL"] != "https://ww.example.com" {
		t.Fatalf("public: %v", env)
	}
	if env["WILLIWAW_SOCIAL_CDN_URL"] != "http://social.stack.svc:13085" {
		t.Fatalf("social sibling port should follow YAML: %v", env)
	}
	if env["WILLIWAW_ACK_CHAT_URL"] != "http://ack.stack.svc:13083" {
		t.Fatalf("ack sibling port: %v", env)
	}
}

func TestLoadStackConfig_DomainAndKernelPeers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	body := `
namespace: demo
domain: example.com
public_scheme: https
barges:
  - name: ultimate_db
    node_id: udb-a
    peers: "udb-b=http://ultimate-db-b.demo.svc:8480"
  - name: williwaw
    domain: williwaw.example.com
    replicas: 1
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadStackConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != "example.com" || got.PublicScheme != "https" {
		t.Fatalf("stack domain: %+v", got)
	}
	var udb, ww *stackApp
	for i := range got.Apps {
		switch got.Apps[i].Name {
		case "ultimate-db":
			udb = &got.Apps[i]
		case "williwaw":
			ww = &got.Apps[i]
		}
	}
	if udb == nil || udb.NodeID != "udb-a" || !strings.Contains(udb.Peers, "udb-b=") {
		t.Fatalf("kernel peers: %+v", udb)
	}
	if ww == nil || ww.Domain != "williwaw.example.com" || ww.StackDomain != "example.com" {
		t.Fatalf("williwaw domain: %+v", ww)
	}
	pub := resolveBargePublicURL(*ww, got.Namespace)
	if pub != "https://williwaw.example.com" {
		t.Fatalf("public url %s", pub)
	}
}

func TestStackKernelEnv_YAMLPort(t *testing.T) {
	env := stackKernelEnv("demo")
	if env["ULTIMATE_DB_URL"] != "http://ultimate-db.demo.svc:8480" {
		t.Fatal(env)
	}
	env = stackKernelEnv("demo", stackApp{Name: "ultimate-db", Port: 18480}, stackApp{Name: "ultimate-keystore", Port: 18481})
	if env["ULTIMATE_DB_URL"] != "http://ultimate-db.demo.svc:18480" || env["ULTIMATE_KEYSTORE_URL"] != "http://ultimate-keystore.demo.svc:18481" {
		t.Fatal(env)
	}
}
