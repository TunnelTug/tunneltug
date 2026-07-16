// Package bgp announces and withdraws anycast prefixes based on health,
// with optional RPKI ROV gating and in-process BGPsec origin signing.
package bgp

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tunneltug/anycast/bgpsec"
	"tunneltug/anycast/config"
	"tunneltug/anycast/rov"
)

// Route is a single anycast prefix announcement.
type Route struct {
	Prefix      string
	NextHop     string
	LocalASN    uint32
	PeerASN     uint32
	Communities []string
	// BGPsec origin signature (nil if signing disabled or failed without require).
	BGPsec *bgpsec.OriginSignature
	// ROV result for this prefix at last prepare.
	ROVState  string
	ROVDetail string
}

// Backend applies announce/withdraw to a BGP speaker or control channel.
type Backend interface {
	Name() string
	Announce(routes []Route) error
	Withdraw(routes []Route) error
	Close() error
}

// Manager tracks announced state, security gates, and drives a Backend.
type Manager struct {
	mu         sync.Mutex
	backend    Backend
	routes     []Route
	announced  bool
	lastErr    string
	lastChange time.Time
	lastPrep   string

	cfg        *config.Config
	rov        *rov.Validator
	signer     *bgpsec.Signer
	failClosed bool
	targetASN  uint32
	sigFile    string

	// Last successful signatures for status API.
	lastSigs []*bgpsec.OriginSignature
}

// NewManager builds a Manager from config (loads ROV + BGPsec signer when enabled).
func NewManager(cfg *config.Config) (*Manager, error) {
	backend, err := NewBackend(cfg)
	if err != nil {
		return nil, err
	}
	routes := make([]Route, 0, len(cfg.BGP.Prefixes))
	for _, p := range cfg.BGP.Prefixes {
		routes = append(routes, Route{
			Prefix:      p,
			NextHop:     cfg.BGP.NextHop,
			LocalASN:    cfg.BGP.LocalASN,
			PeerASN:     cfg.BGP.PeerASN,
			Communities: append([]string{}, cfg.BGP.Communities...),
		})
	}

	m := &Manager{
		backend:    backend,
		routes:     routes,
		cfg:        cfg,
		failClosed: cfg.FailClosed(),
		sigFile:    strings.TrimSpace(cfg.BGP.Security.BGPsec.SignatureFile),
	}

	// ROV
	sec := cfg.BGP.Security
	roas := make([]rov.ROA, 0, len(sec.ROV.ROAs))
	for _, r := range sec.ROV.ROAs {
		roas = append(roas, rov.ROA{Prefix: r.Prefix, ASN: r.ASN, MaxLength: r.MaxLength})
	}
	v, err := rov.New(rov.Config{
		Enabled:      sec.ROV.Enabled,
		RequireValid: sec.ROV.RequireValid,
		AllowPrivate: sec.ROV.AllowPrivate,
		ROAs:         roas,
		ROAFile:      sec.ROV.ROAFile,
	})
	if err != nil {
		return nil, fmt.Errorf("rov: %w", err)
	}
	m.rov = v

	// BGPsec signer (RPKI router key — not ACME TLS key)
	if sec.BGPsec.Enabled {
		target := sec.BGPsec.TargetASN
		if target == 0 {
			target = cfg.BGP.PeerASN
		}
		if target == 0 {
			return nil, fmt.Errorf("bgpsec: target_asn / peer_asn required")
		}
		m.targetASN = target
		signer, err := bgpsec.LoadSigner(sec.BGPsec.PrivateKey, sec.BGPsec.SKI, cfg.BGP.LocalASN)
		if err != nil {
			return nil, err
		}
		m.signer = signer
		log.Printf("[bgpsec] loaded router key ski=%s origin_asn=%d target_asn=%d",
			signer.SKIHex(), cfg.BGP.LocalASN, target)
	}

	return m, nil
}

// NewBackend selects an implementation from config.
func NewBackend(cfg *config.Config) (Backend, error) {
	switch strings.ToLower(cfg.BGP.Backend) {
	case "log":
		return NewLogBackend(), nil
	case "exabgp":
		return NewExaBGPBackend(cfg.BGP.ExaBGP.CommandPath), nil
	case "bird":
		return NewBirdBackend(cfg.BGP.Bird), nil
	case "file":
		return NewFileBackend(cfg.BGP.File.Path), nil
	default:
		return nil, fmt.Errorf("unknown bgp backend %q", cfg.BGP.Backend)
	}
}

// prepareRoutes runs ROV + BGPsec signing on a copy of routes. Does not announce.
func (m *Manager) prepareRoutes() ([]Route, error) {
	out := make([]Route, len(m.routes))
	copy(out, m.routes)
	var sigs []*bgpsec.OriginSignature
	var issues []string

	for i := range out {
		r := &out[i]
		r.BGPsec = nil
		r.ROVState = ""
		r.ROVDetail = ""

		// ROV gate
		if m.rov != nil {
			ok, val, detail := m.rov.AllowAnnounce(r.Prefix, r.LocalASN)
			r.ROVState = val.String()
			r.ROVDetail = detail
			if !ok {
				msg := fmt.Sprintf("%s ROV %s: %s", r.Prefix, val, detail)
				issues = append(issues, msg)
				if m.failClosed || (m.cfg != nil && m.cfg.BGP.Security.ROV.RequireValid) {
					return nil, fmt.Errorf("rov reject: %s", msg)
				}
				log.Printf("[rov] WARN allow despite: %s", msg)
			}
		}

		// BGPsec origin sign in-process
		if m.signer != nil {
			sig, err := m.signer.SignOrigin(r.Prefix, m.targetASN)
			if err != nil {
				msg := fmt.Sprintf("%s bgpsec sign: %v", r.Prefix, err)
				issues = append(issues, msg)
				req := m.cfg != nil && m.cfg.BGP.Security.BGPsec.RequireSign
				if m.failClosed || req {
					return nil, fmt.Errorf("%s", msg)
				}
				log.Printf("[bgpsec] WARN %s", msg)
				continue
			}
			if !m.signer.VerifyOrigin(sig) {
				msg := fmt.Sprintf("%s bgpsec self-verify failed", r.Prefix)
				if m.failClosed || (m.cfg != nil && m.cfg.BGP.Security.BGPsec.RequireSign) {
					return nil, fmt.Errorf("%s", msg)
				}
				log.Printf("[bgpsec] WARN %s", msg)
				continue
			}
			r.BGPsec = sig
			sigs = append(sigs, sig)
		} else if m.cfg != nil && m.cfg.BGP.Security.BGPsec.RequireSign {
			return nil, fmt.Errorf("bgpsec require_sign but signer not loaded")
		}
	}

	if len(issues) > 0 {
		m.lastPrep = strings.Join(issues, "; ")
	} else {
		m.lastPrep = "ok"
	}
	m.lastSigs = sigs
	return out, nil
}

// SetHealthy announces when healthy=true and not already announced; withdraws otherwise.
// Announce path: ROV allow → BGPsec sign → backend.
func (m *Manager) SetHealthy(healthy bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if healthy {
		if m.announced {
			return nil
		}
		prepared, err := m.prepareRoutes()
		if err != nil {
			m.lastErr = err.Error()
			log.Printf("[bgp] announce blocked by security: %v", err)
			return err
		}
		if err := m.writeSignatureExport(prepared); err != nil {
			m.lastErr = err.Error()
			if m.failClosed {
				return err
			}
			log.Printf("[bgpsec] signature file: %v", err)
		}
		if err := m.backend.Announce(prepared); err != nil {
			m.lastErr = err.Error()
			return err
		}
		// Keep prepared signatures on live routes for status.
		m.routes = prepared
		m.announced = true
		m.lastErr = ""
		m.lastChange = time.Now().UTC()
		signed := 0
		for _, r := range prepared {
			if r.BGPsec != nil {
				signed++
			}
		}
		log.Printf("[bgp] announced %d prefix(es) via %s (bgpsec_signed=%d rov_prep=%s)",
			len(prepared), m.backend.Name(), signed, m.lastPrep)
		return nil
	}

	if !m.announced {
		return nil
	}
	if err := m.backend.Withdraw(m.routes); err != nil {
		m.lastErr = err.Error()
		return err
	}
	// Clear signatures on withdraw.
	for i := range m.routes {
		m.routes[i].BGPsec = nil
	}
	m.lastSigs = nil
	m.announced = false
	m.lastErr = ""
	m.lastChange = time.Now().UTC()
	log.Printf("[bgp] withdrew %d prefix(es) via %s", len(m.routes), m.backend.Name())
	return nil
}

func (m *Manager) writeSignatureExport(routes []Route) error {
	if m.sigFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.sigFile), 0o755); err != nil {
		return err
	}
	var lines []string
	for _, r := range routes {
		if r.BGPsec == nil {
			continue
		}
		b, err := json.Marshal(r.BGPsec.Summary())
		if err != nil {
			return err
		}
		lines = append(lines, string(b))
	}
	return os.WriteFile(m.sigFile, []byte(strings.Join(lines, "\n")+"\n"), 0o640)
}

// ForceWithdraw withdraws regardless of prior state (shutdown).
func (m *Manager) ForceWithdraw() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.backend.Withdraw(m.routes)
	m.announced = false
	m.lastChange = time.Now().UTC()
	m.lastSigs = nil
	for i := range m.routes {
		m.routes[i].BGPsec = nil
	}
	if err != nil {
		m.lastErr = err.Error()
	} else {
		m.lastErr = ""
	}
	return err
}

// Status returns a snapshot for the HTTP API.
func (m *Manager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefixes := make([]string, 0, len(m.routes))
	routeStatus := make([]map[string]any, 0, len(m.routes))
	for _, r := range m.routes {
		prefixes = append(prefixes, r.Prefix)
		rs := map[string]any{
			"prefix":     r.Prefix,
			"rov":        r.ROVState,
			"rov_detail": r.ROVDetail,
			"signed":     r.BGPsec != nil,
		}
		if r.BGPsec != nil {
			rs["bgpsec"] = r.BGPsec.Summary()
		}
		routeStatus = append(routeStatus, rs)
	}
	sec := map[string]any{
		"fail_closed": m.failClosed,
		"rov":         m.rov.Status(),
		"prep":        m.lastPrep,
	}
	if m.signer != nil {
		sec["bgpsec"] = map[string]any{
			"enabled":    true,
			"ski":        m.signer.SKIHex(),
			"origin_asn": m.signer.ASN(),
			"target_asn": m.targetASN,
			"algorithm":  "suite-1-sha256-ecdsa-p256",
			"note":       "RPKI router key (not ACME TLS key)",
		}
	} else {
		sec["bgpsec"] = map[string]any{"enabled": false}
	}
	if len(m.lastSigs) > 0 {
		summaries := make([]map[string]any, 0, len(m.lastSigs))
		for _, s := range m.lastSigs {
			summaries = append(summaries, s.Summary())
		}
		sec["signatures"] = summaries
	}
	return map[string]any{
		"backend":     m.backend.Name(),
		"announced":   m.announced,
		"prefixes":    prefixes,
		"routes":      routeStatus,
		"security":    sec,
		"last_error":  m.lastErr,
		"last_change": m.lastChange,
	}
}

// Close withdraws and closes the backend.
func (m *Manager) Close() error {
	_ = m.ForceWithdraw()
	return m.backend.Close()
}

// FormatExaBGPAnnounce builds an ExaBGP API announce line.
func FormatExaBGPAnnounce(r Route) string {
	var b strings.Builder
	b.WriteString("announce route ")
	b.WriteString(r.Prefix)
	b.WriteString(" next-hop ")
	b.WriteString(r.NextHop)
	if r.LocalASN != 0 {
		fmt.Fprintf(&b, " origin igp as-path [ %d ]", r.LocalASN)
	}
	for _, c := range r.Communities {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		fmt.Fprintf(&b, " community [ %s ]", c)
	}
	// Attribute comment for operators / custom ExaBGP processes (not standard attribute injection).
	if r.BGPsec != nil {
		fmt.Fprintf(&b, " # bgpsec-ski=%s bgpsec-sig=%s",
			hex.EncodeToString(r.BGPsec.SKI),
			hex.EncodeToString(r.BGPsec.Signature))
	}
	return b.String()
}

// FormatExaBGPWithdraw builds an ExaBGP API withdraw line.
func FormatExaBGPWithdraw(r Route) string {
	return "withdraw route " + r.Prefix + " next-hop " + r.NextHop
}
