package bgp

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileBackend writes a simple route state file for external automations.
type FileBackend struct {
	path string
	mu   sync.Mutex
}

func NewFileBackend(path string) *FileBackend {
	if strings.TrimSpace(path) == "" {
		path = "state/announced.routes"
	}
	return &FileBackend{path: path}
}

func (b *FileBackend) Name() string { return "file" }

func (b *FileBackend) Announce(routes []Route) error {
	return b.write(routes, true)
}

func (b *FileBackend) Withdraw(routes []Route) error {
	return b.write(routes, false)
}

func (b *FileBackend) write(routes []Route, announce bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	var bld strings.Builder
	fmt.Fprintf(&bld, "# tunneltug anycast %s state=%s\n", time.Now().UTC().Format(time.RFC3339), map[bool]string{true: "announced", false: "withdrawn"}[announce])
	if announce {
		for _, r := range routes {
			fmt.Fprintf(&bld, "%s via %s asn %d rov %s", r.Prefix, r.NextHop, r.LocalASN, r.ROVState)
			if r.BGPsec != nil {
				fmt.Fprintf(&bld, " bgpsec_ski %s bgpsec_sig %s bgpsec_path %s",
					hex.EncodeToString(r.BGPsec.SKI),
					hex.EncodeToString(r.BGPsec.Signature),
					hex.EncodeToString(r.BGPsec.BGPsecPathWire))
			}
			bld.WriteByte('\n')
		}
	}
	return os.WriteFile(b.path, []byte(bld.String()), 0o640)
}

func (b *FileBackend) Close() error { return nil }
