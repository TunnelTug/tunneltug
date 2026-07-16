package zonepack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadVIPExpand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "z.json")
	raw := `{
  "host": "ns.example",
  "private_suffixes": [".com"],
  "records": [
    {"domain": "www.example.com", "type": "A", "value": "{{VIP}}", "ttl": 60}
  ]
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	pack, err := Load(path, "203.0.113.53")
	if err != nil {
		t.Fatal(err)
	}
	if pack.Records != 1 {
		t.Fatalf("records: %d", pack.Records)
	}
	if pack.Snap.Records[0].Value != "203.0.113.53" {
		t.Fatalf("vip: %q", pack.Snap.Records[0].Value)
	}
}
