package bgp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatExaBGPAnnounce(t *testing.T) {
	line := FormatExaBGPAnnounce(Route{
		Prefix:      "203.0.113.53/32",
		NextHop:     "203.0.113.53",
		LocalASN:    65001,
		Communities: []string{"65001:53"},
	})
	if !strings.Contains(line, "announce route 203.0.113.53/32") {
		t.Fatalf("missing announce: %s", line)
	}
	if !strings.Contains(line, "next-hop 203.0.113.53") {
		t.Fatalf("missing next-hop: %s", line)
	}
	if !strings.Contains(line, "community [ 65001:53 ]") {
		t.Fatalf("missing community: %s", line)
	}
}

func TestFormatExaBGPWithdraw(t *testing.T) {
	line := FormatExaBGPWithdraw(Route{Prefix: "203.0.113.53/32", NextHop: "203.0.113.53"})
	if line != "withdraw route 203.0.113.53/32 next-hop 203.0.113.53" {
		t.Fatalf("unexpected: %s", line)
	}
}

func TestManagerAnnounceWithdraw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes")
	backend := NewFileBackend(path)
	m := &Manager{
		backend: backend,
		routes: []Route{{
			Prefix:   "203.0.113.53/32",
			NextHop:  "203.0.113.53",
			LocalASN: 65001,
		}},
	}
	if err := m.SetHealthy(true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "203.0.113.53/32") {
		t.Fatalf("expected prefix in file: %s", raw)
	}
	if err := m.SetHealthy(false); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "203.0.113.53/32 via") {
		t.Fatalf("expected withdraw (no via line): %s", raw)
	}
}

func TestBirdRender(t *testing.T) {
	bb := &BirdBackend{protocol: "anycast4"}
	body := bb.render([]Route{{Prefix: "203.0.113.53/32", NextHop: "203.0.113.53"}}, true)
	if !strings.Contains(body, "route 203.0.113.53/32 via 203.0.113.53;") {
		t.Fatalf("bad bird body: %s", body)
	}
	empty := bb.render(nil, false)
	if strings.Contains(empty, "route ") {
		t.Fatalf("withdraw should not include routes: %s", empty)
	}
}
