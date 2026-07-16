// Package updater ties health probes to BGP announce/withdraw for TLD anycast.
package updater

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/0TrustCloud/secure_dns"

	"tunneltug/anycast/bgp"
	"tunneltug/anycast/config"
	"tunneltug/anycast/health"
	"tunneltug/anycast/origin"
	"tunneltug/anycast/splitdns"
	"tunneltug/anycast/zonesync"
)

// Updater is the main control loop.
type Updater struct {
	cfg     *config.Config
	bgp     *bgp.Manager
	prober  *health.Prober
	tracker *health.Tracker
	dns     *splitdns.Server
	syncer  *zonesync.Syncer
	origin  *origin.Server

	mu      sync.Mutex
	started time.Time
}

// New constructs an Updater from config.
func New(cfg *config.Config) (*Updater, error) {
	mgr, err := bgp.NewManager(cfg)
	if err != nil {
		return nil, err
	}
	u := &Updater{
		cfg:     cfg,
		bgp:     mgr,
		prober:  health.NewProber(cfg.Health),
		tracker: health.NewTracker(cfg.Health),
		dns:     splitdns.New(cfg),
	}
	if cfg.Origin.Enabled {
		u.origin = origin.New(cfg.Origin, cfg.NodeID)
	}
	u.syncer = zonesync.New(cfg.Sync, func(snap secure_dns.ZoneSnapshot) {
		if u.dns != nil {
			u.dns.LoadSnapshot(snap)
		}
	})
	return u, nil
}

// Run starts DNS, origin, sync, status API, and the health→BGP loop until ctx cancel.
func (u *Updater) Run(ctx context.Context) error {
	u.started = time.Now().UTC()

	if u.cfg.DNS.Enabled {
		if err := u.dns.Start(); err != nil {
			return err
		}
		defer u.dns.Shutdown()
	}

	if u.origin != nil {
		if err := u.origin.Start(ctx); err != nil {
			return err
		}
		defer func() {
			shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = u.origin.Shutdown(shCtx)
		}()
	}

	go u.syncer.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", u.handleHealth)
	mux.HandleFunc("/status", u.handleStatus)
	mux.HandleFunc("/ready", u.handleReady)
	srv := &http.Server{
		Addr:              u.cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("[api] status listening on %s", u.cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[api] listen error: %v", err)
		}
	}()
	defer func() {
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()

	log.Printf("[updater] node=%s tlds=%v zone=%s bgp=%s prefixes=%v",
		u.cfg.NodeID, u.cfg.TLDs, u.cfg.Zone, u.cfg.BGP.Backend, u.cfg.BGP.Prefixes)

	u.tick(ctx)

	t := time.NewTicker(u.cfg.Health.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[updater] shutting down — withdrawing anycast prefixes")
			_ = u.bgp.ForceWithdraw()
			_ = u.bgp.Close()
			return nil
		case <-t.C:
			u.tick(ctx)
		}
	}
}

func (u *Updater) tick(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, u.cfg.Health.Timeout+time.Second)
	defer cancel()
	res := u.prober.Check(pctx)
	healthy, changed := u.tracker.Observe(res)
	if changed {
		log.Printf("[updater] health changed → healthy=%v detail=%s", healthy, res.Detail)
	}
	if err := u.bgp.SetHealthy(healthy); err != nil {
		log.Printf("[bgp] set healthy=%v: %v", healthy, err)
	}
}

func (u *Updater) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := u.tracker.Status()
	ok, _ := st["healthy"].(bool)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"healthy": ok,
		"node":    u.cfg.NodeID,
	})
}

func (u *Updater) handleReady(w http.ResponseWriter, r *http.Request) {
	bgpSt := u.bgp.Status()
	announced, _ := bgpSt["announced"].(bool)
	if !u.tracker.Healthy() || !announced {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ready":     false,
			"healthy":   u.tracker.Healthy(),
			"announced": announced,
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "healthy": true, "announced": true})
}

func (u *Updater) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(u.Status())
}

// Status aggregates component state.
func (u *Updater) Status() map[string]any {
	out := map[string]any{
		"node_id":  u.cfg.NodeID,
		"tlds":     u.cfg.TLDs,
		"zone":     u.cfg.Zone,
		"ns_host":  u.cfg.NSHost,
		"started":  u.started,
		"uptime_s": int(time.Since(u.started).Seconds()),
		"health":   u.tracker.Status(),
		"bgp":      u.bgp.Status(),
		"dns":      u.dns.Status(),
		"sync":     u.syncer.Status(),
		"security": map[string]any{
			"rov_enabled":    u.cfg.BGP.Security.ROV.Enabled,
			"bgpsec_enabled": u.cfg.BGP.Security.BGPsec.Enabled,
			"fail_closed":    u.cfg.FailClosed(),
			"note":           "BGPsec uses RPKI router key; ACME TLS keys are separate",
		},
	}
	if u.origin != nil {
		out["origin"] = u.origin.Status()
	}
	return out
}
