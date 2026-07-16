// Package health probes local split-horizon DNS and optional HTTP/TCP targets.
package health

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tunneltug/anycast/config"
)

// Result is one probe cycle outcome.
type Result struct {
	OK       bool              `json:"ok"`
	Detail   string            `json:"detail,omitempty"`
	Checks   map[string]string `json:"checks,omitempty"`
	CheckedAt time.Time        `json:"checked_at"`
}

// Tracker applies fail/recover thresholds to consecutive probe results.
type Tracker struct {
	mu               sync.Mutex
	failThreshold    int
	recoverThreshold int
	failCount        int
	recoverCount     int
	healthy          bool // BGP-facing state (after thresholds)
	last             Result
}

func NewTracker(cfg config.HealthConfig) *Tracker {
	return &Tracker{
		failThreshold:    cfg.FailThreshold,
		recoverThreshold: cfg.RecoverThreshold,
	}
}

// Observe records a probe result and returns whether the service is healthy for BGP.
func (t *Tracker) Observe(r Result) (healthy bool, changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = r
	prev := t.healthy
	if r.OK {
		t.failCount = 0
		t.recoverCount++
		if !t.healthy && t.recoverCount >= t.recoverThreshold {
			t.healthy = true
		}
	} else {
		t.recoverCount = 0
		t.failCount++
		if t.healthy && t.failCount >= t.failThreshold {
			t.healthy = false
		}
		// Cold start: stay withdrawn until recover_threshold successes.
		if !t.healthy {
			// already false
		}
	}
	return t.healthy, t.healthy != prev
}

// Status returns tracker state for the API.
func (t *Tracker) Status() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{
		"healthy":         t.healthy,
		"fail_count":      t.failCount,
		"recover_count":   t.recoverCount,
		"fail_threshold":  t.failThreshold,
		"recover_threshold": t.recoverThreshold,
		"last":            t.last,
	}
}

// Healthy reports the threshold-gated health state.
func (t *Tracker) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.healthy
}

// Prober runs configured checks.
type Prober struct {
	cfg    config.HealthConfig
	client *http.Client
}

func NewProber(cfg config.HealthConfig) *Prober {
	return &Prober{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}
}

// Check runs all enabled probes once.
func (p *Prober) Check(ctx context.Context) Result {
	checks := make(map[string]string)
	var failures []string

	if p.cfg.DNSProbe.Enabled {
		if err := p.checkDNS(ctx); err != nil {
			checks["dns"] = err.Error()
			failures = append(failures, "dns: "+err.Error())
		} else {
			checks["dns"] = "ok"
		}
	}

	for i, hp := range p.cfg.HTTPProbes {
		key := fmt.Sprintf("http[%d]", i)
		if err := p.checkHTTP(ctx, hp); err != nil {
			checks[key] = err.Error()
			failures = append(failures, key+": "+err.Error())
		} else {
			checks[key] = "ok"
		}
	}

	for i, addr := range p.cfg.TCPProbes {
		key := fmt.Sprintf("tcp[%d]", i)
		if err := p.checkTCP(ctx, addr); err != nil {
			checks[key] = err.Error()
			failures = append(failures, key+": "+err.Error())
		} else {
			checks[key] = "ok"
		}
	}

	// If nothing is configured, treat as healthy so dry-run can announce.
	if !p.cfg.DNSProbe.Enabled && len(p.cfg.HTTPProbes) == 0 && len(p.cfg.TCPProbes) == 0 {
		return Result{OK: true, Detail: "no probes configured", Checks: checks, CheckedAt: time.Now().UTC()}
	}

	if len(failures) > 0 {
		return Result{
			OK:        false,
			Detail:    strings.Join(failures, "; "),
			Checks:    checks,
			CheckedAt: time.Now().UTC(),
		}
	}
	return Result{OK: true, Detail: "all checks passed", Checks: checks, CheckedAt: time.Now().UTC()}
}

func (p *Prober) checkHTTP(ctx context.Context, hp config.HTTPProbe) error {
	if strings.TrimSpace(hp.URL) == "" {
		return fmt.Errorf("empty url")
	}
	want := hp.ExpectStatus
	if want == 0 {
		want = 200
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hp.URL, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != want {
		return fmt.Errorf("status %d want %d", resp.StatusCode, want)
	}
	return nil
}

func (p *Prober) checkTCP(ctx context.Context, addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("empty address")
	}
	d := net.Dialer{Timeout: p.cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func (p *Prober) checkDNS(ctx context.Context) error {
	target := strings.TrimSpace(p.cfg.DNSProbe.Target)
	if target == "" {
		return fmt.Errorf("dns probe target empty")
	}
	if !strings.Contains(target, ":") {
		target += ":53"
	}
	names := p.cfg.DNSProbe.Names
	if len(names) == 0 {
		return fmt.Errorf("no dns probe names")
	}
	types := p.cfg.DNSProbe.ExpectTypes
	if len(types) == 0 {
		types = []string{"A"}
	}

	var lastErr error
	answered := 0
	for _, name := range names {
		name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		if name == "" {
			continue
		}
		// Try each type until one succeeds for this name.
		ok := false
		for _, typ := range types {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := queryDNS(ctx, target, name, typ, p.cfg.Timeout); err != nil {
				lastErr = err
				continue
			}
			ok = true
			break
		}
		if ok {
			answered++
		}
	}
	if answered == 0 {
		if lastErr != nil {
			return fmt.Errorf("no answers: %w", lastErr)
		}
		return fmt.Errorf("no answers for probe names")
	}
	// Require at least one name to resolve; partial is OK (NS vs A mix).
	return nil
}

func queryDNS(ctx context.Context, server, name, qtype string, timeout time.Duration) error {
	qtype = strings.ToUpper(strings.TrimSpace(qtype))
	typeNum := wireType(qtype)
	if typeNum == 0 {
		return fmt.Errorf("unsupported type %s", qtype)
	}
	packet := buildQuery(name, typeNum)

	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(packet); err != nil {
		return err
	}
	buf := make([]byte, 1232)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	if n < 12 {
		return fmt.Errorf("short response")
	}
	flags := binary.BigEndian.Uint16(buf[2:4])
	rcode := flags & 0x0F
	ancount := binary.BigEndian.Uint16(buf[6:8])
	// Accept NOERROR with answers, or NOERROR NODATA (name exists).
	if rcode == 3 { // NXDOMAIN
		return fmt.Errorf("NXDOMAIN for %s %s", name, qtype)
	}
	if rcode != 0 {
		return fmt.Errorf("rcode %d for %s %s", rcode, name, qtype)
	}
	if ancount == 0 {
		// NODATA is weak success only for existence probes of private TLD apex with other types.
		// Prefer real answers for anycast gate.
		return fmt.Errorf("NODATA for %s %s", name, qtype)
	}
	return nil
}

func wireType(name string) uint16 {
	switch strings.ToUpper(name) {
	case "A":
		return 1
	case "NS":
		return 2
	case "CNAME":
		return 5
	case "PTR":
		return 12
	case "MX":
		return 15
	case "TXT":
		return 16
	case "AAAA":
		return 28
	default:
		return 0
	}
}

func buildQuery(name string, qtype uint16) []byte {
	// Standard recursive query; authoritative servers ignore RD.
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], 0x1337) // ID
	binary.BigEndian.PutUint16(msg[2:4], 0x0100) // RD
	binary.BigEndian.PutUint16(msg[4:6], 1)      // QDCOUNT

	for _, label := range strings.Split(name, ".") {
		if label == "" {
			continue
		}
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)
	typeTTL := make([]byte, 4)
	binary.BigEndian.PutUint16(typeTTL[0:2], qtype)
	binary.BigEndian.PutUint16(typeTTL[2:4], 1) // IN
	msg = append(msg, typeTTL...)
	return msg
}
