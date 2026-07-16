package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tunneltug/anycast/config"
)

func TestTrackerThresholds(t *testing.T) {
	tr := NewTracker(config.HealthConfig{FailThreshold: 2, RecoverThreshold: 2})
	// Cold start unhealthy
	if tr.Healthy() {
		t.Fatal("expected cold start unhealthy")
	}
	h, ch := tr.Observe(Result{OK: true})
	if h || ch {
		t.Fatalf("need 2 successes, got healthy=%v changed=%v", h, ch)
	}
	h, ch = tr.Observe(Result{OK: true})
	if !h || !ch {
		t.Fatalf("expected recover healthy=%v changed=%v", h, ch)
	}
	h, ch = tr.Observe(Result{OK: false})
	if !h || ch {
		t.Fatalf("single fail should keep healthy, got healthy=%v changed=%v", h, ch)
	}
	h, ch = tr.Observe(Result{OK: false})
	if h || !ch {
		t.Fatalf("expected withdraw healthy=%v changed=%v", h, ch)
	}
}

func TestHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := NewProber(config.HealthConfig{
		Timeout: 2 * time.Second,
		HTTPProbes: []config.HTTPProbe{
			{URL: srv.URL, ExpectStatus: 200},
		},
	})
	res := p.Check(context.Background())
	if !res.OK {
		t.Fatalf("expected ok: %s", res.Detail)
	}
}

func TestDNSProbeAgainstLocalResponder(t *testing.T) {
	// Minimal UDP DNS that answers A for example.test with 127.0.0.1
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			// Build a crude NOERROR + 1 A answer by echoing question and appending A RR.
			req := buf[:n]
			// Find end of question
			idx := 12
			for idx < n && req[idx] != 0 {
				idx += 1 + int(req[idx])
			}
			if idx+5 > n {
				continue
			}
			qend := idx + 5 // null + type + class
			resp := make([]byte, 0, n+16)
			resp = append(resp, req[:12]...)
			resp[2] = 0x81 // QR RD RA-ish
			resp[3] = 0x80
			resp[6] = 0
			resp[7] = 1 // ANCOUNT=1
			resp = append(resp, req[12:qend]...)
			// name pointer to offset 12
			resp = append(resp, 0xc0, 0x0c)
			resp = append(resp, 0x00, 0x01)       // A
			resp = append(resp, 0x00, 0x01)       // IN
			resp = append(resp, 0x00, 0x00, 0x00, 0x3c) // TTL 60
			resp = append(resp, 0x00, 0x04)       // RDLENGTH
			resp = append(resp, 127, 0, 0, 1)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()

	p := NewProber(config.HealthConfig{
		Timeout: 2 * time.Second,
		DNSProbe: config.DNSProbe{
			Enabled:     true,
			Target:      pc.LocalAddr().String(),
			Names:       []string{"example.test"},
			ExpectTypes: []string{"A"},
		},
	})
	res := p.Check(context.Background())
	if !res.OK {
		t.Fatalf("dns probe failed: %s", res.Detail)
	}
}
