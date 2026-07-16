// Package origin serves an optional HTTP face on the anycast edge.
package origin

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config binds the origin face.
type Config struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Listen  string `yaml:"listen" json:"listen"`
	// Banner is optional response title text.
	Banner string `yaml:"banner" json:"banner"`
}

// Server is a minimal HTTP origin for traffic steered to the edge VIP.
type Server struct {
	cfg  Config
	node string
	mu   sync.Mutex
	srv  *http.Server
}

// New builds an origin server (Start must be called).
func New(cfg Config, nodeID string) *Server {
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	return &Server{cfg: cfg, node: nodeID}
}

// Start serves until ctx cancel.
func (s *Server) Start(ctx context.Context) error {
	if s == nil || !s.cfg.Enabled {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()

	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("origin listen %s: %w", s.cfg.Listen, err)
	}
	log.Printf("[origin] listening on %s", s.cfg.Listen)

	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[origin] serve: %v", err)
		}
	}()
	return nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	title := s.cfg.Banner
	if title == "" {
		title = host
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Anycast-Node", s.node)
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>%s</title></head>
<body>
<h1>%s</h1>
<p>Host: <code>%s</code> Path: <code>%s</code> Node: <code>%s</code></p>
</body></html>`, title, title, host, r.URL.Path, s.node)
}

// Status for the status API.
func (s *Server) Status() map[string]any {
	if s == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled": s.cfg.Enabled,
		"listen":  s.cfg.Listen,
		"banner":  s.cfg.Banner,
	}
}

// Shutdown stops the origin if running.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}
