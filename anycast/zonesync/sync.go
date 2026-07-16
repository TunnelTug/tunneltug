// Package zonesync pulls secure_dns.ZoneSnapshot from TunnelTug / platform nameservice.
package zonesync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/0TrustCloud/secure_dns"

	"tunneltug/anycast/config"
)

// Syncer periodically pulls zone snapshots.
type Syncer struct {
	cfg    config.SyncConfig
	client *http.Client
	mu     sync.RWMutex
	last   *secure_dns.ZoneSnapshot
	lastAt time.Time
	lastErr string
	onSnap func(secure_dns.ZoneSnapshot)
}

func New(cfg config.SyncConfig, onSnap func(secure_dns.ZoneSnapshot)) *Syncer {
	return &Syncer{
		cfg: cfg,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		onSnap: onSnap,
	}
}

// Run blocks until ctx is done, syncing on interval.
func (s *Syncer) Run(ctx context.Context) {
	if !s.cfg.Enabled || strings.TrimSpace(s.cfg.URL) == "" {
		return
	}
	// Immediate first pull.
	if err := s.Pull(ctx); err != nil {
		log.Printf("[sync] initial pull failed: %v", err)
	}
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Pull(ctx); err != nil {
				log.Printf("[sync] pull failed: %v", err)
			}
		}
	}
}

// Pull fetches one snapshot.
func (s *Syncer) Pull(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.URL, nil)
	if err != nil {
		return err
	}
	if tok := strings.TrimSpace(s.cfg.Token); tok != "" {
		req.Header.Set("X-0Trust-Sync-Token", tok)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.setErr(err)
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		s.setErr(err)
		return err
	}
	var snap secure_dns.ZoneSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		s.setErr(err)
		return fmt.Errorf("decode snapshot: %w", err)
	}
	s.mu.Lock()
	s.last = &snap
	s.lastAt = time.Now().UTC()
	s.lastErr = ""
	s.mu.Unlock()
	if s.onSnap != nil {
		s.onSnap(snap)
	}
	log.Printf("[sync] zone snapshot OK — %d records host=%s suffixes=%v",
		len(snap.Records), snap.Host, snap.PrivateSuffixes)
	return nil
}

func (s *Syncer) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err.Error()
	}
}

// Status returns sync state for the API.
func (s *Syncer) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]any{
		"enabled":   s.cfg.Enabled,
		"url":       s.cfg.URL,
		"last_at":   s.lastAt,
		"last_error": s.lastErr,
	}
	if s.last != nil {
		out["records"] = len(s.last.Records)
		out["host"] = s.last.Host
		out["private_suffixes"] = s.last.PrivateSuffixes
		out["version"] = s.last.Version
	}
	return out
}

// Last returns the most recent snapshot (or nil).
func (s *Syncer) Last() *secure_dns.ZoneSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}
