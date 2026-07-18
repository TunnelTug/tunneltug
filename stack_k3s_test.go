package main

import (
	"strings"
	"testing"
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
