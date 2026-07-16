package bgp

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// ExaBGPBackend writes ExaBGP API commands to a path (FIFO, pipe, or file).
// Point command_path at ExaBGP's exabgp.api / process stdin file as deployed.
type ExaBGPBackend struct {
	path string
	mu   sync.Mutex
}

func NewExaBGPBackend(path string) *ExaBGPBackend {
	if strings.TrimSpace(path) == "" {
		path = "exabgp.cmd"
	}
	return &ExaBGPBackend{path: path}
}

func (b *ExaBGPBackend) Name() string { return "exabgp" }

func (b *ExaBGPBackend) Announce(routes []Route) error {
	return b.write(routes, true)
}

func (b *ExaBGPBackend) Withdraw(routes []Route) error {
	return b.write(routes, false)
}

func (b *ExaBGPBackend) write(routes []Route, announce bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var lines []string
	for _, r := range routes {
		if announce {
			lines = append(lines, FormatExaBGPAnnounce(r))
		} else {
			lines = append(lines, FormatExaBGPWithdraw(r))
		}
	}
	payload := strings.Join(lines, "\n") + "\n"

	// Special path: write to process stdout for process-mode ExaBGP helpers.
	if b.path == "stdout" || b.path == "-" {
		_, err := fmt.Fprint(os.Stdout, payload)
		return err
	}

	f, err := os.OpenFile(b.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		return fmt.Errorf("exabgp command path %s: %w", b.path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(payload); err != nil {
		return fmt.Errorf("exabgp write: %w", err)
	}
	return nil
}

func (b *ExaBGPBackend) Close() error { return nil }
