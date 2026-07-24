package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/quic-go/quic-go"
)

// Mesh agent gateway: secure_network-compatible QUIC + Noise (ALPN secure-overlay).
// Used by 0TrustOS (gonode) -gateway host:port — distinct from TunnelTug yamux control ALPN "tunneltug".

const meshAgentALPN = "secure-overlay"

// max frame size aligned with secure_network.MaxFrameSize (used only for handshake frames).
const meshAgentMaxFrame = 64 * 1024

type meshAgentGateway struct {
	priv   []byte
	pub    []byte
	cipher noise.CipherSuite
	mu     sync.Mutex
	peers  int
}

func meshAgentKeyPath() string {
	dir := strings.TrimSpace(*meshDataDir)
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".tunneltug", "mesh-agent")
	}
	return dir
}

func loadOrCreateMeshAgentKeys() (priv, pub []byte, err error) {
	dir := meshAgentKeyPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	privPath := filepath.Join(dir, "gateway.noise.priv")
	pubPath := filepath.Join(dir, "gateway.noise.pub")
	if raw, err := os.ReadFile(privPath); err == nil && len(raw) >= 32 {
		priv = raw[:32]
		if pubRaw, err := os.ReadFile(pubPath); err == nil {
			pubRaw = []byte(strings.TrimSpace(string(pubRaw)))
			if dec, err := hex.DecodeString(string(pubRaw)); err == nil && len(dec) == 32 {
				return priv, dec, nil
			}
		}
		// derive is not available; regenerate pair
	}
	cs := noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)
	kp, err := cs.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(privPath, kp.Private, 0o600); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(pubPath, []byte(hex.EncodeToString(kp.Public)+"\n"), 0o644); err != nil {
		return nil, nil, err
	}
	log.Printf("[mesh-agent] wrote gateway noise pubkey %s", pubPath)
	return kp.Private, kp.Public, nil
}

func newMeshAgentGateway() (*meshAgentGateway, error) {
	priv, pub, err := loadOrCreateMeshAgentKeys()
	if err != nil {
		return nil, err
	}
	return &meshAgentGateway{
		priv:   priv,
		pub:    pub,
		cipher: noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256),
	}, nil
}

func (g *meshAgentGateway) PubHex() string {
	if g == nil {
		return ""
	}
	return hex.EncodeToString(g.pub)
}

// dualControlTLS advertises both TunnelTug yamux control and secure-overlay mesh agent.
func dualControlTLS(base *tls.Config) *tls.Config {
	cfg := base.Clone()
	cfg.NextProtos = []string{controlALPN, meshAgentALPN}
	return cfg
}

func listenControlQUICDual(addr string, tlsConfig *tls.Config) (*quic.Listener, error) {
	return quic.ListenAddr(addr, dualControlTLS(tlsConfig), controlQUICConfig())
}

func writeMeshFrame(w io.Writer, payload []byte) error {
	if len(payload) > meshAgentMaxFrame {
		return fmt.Errorf("frame too large")
	}
	hdr := []byte{byte(len(payload) >> 24), byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readMeshFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	size := int(hdr[0])<<24 | int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
	if size <= 0 || size > meshAgentMaxFrame {
		return nil, fmt.Errorf("bad frame size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (g *meshAgentGateway) handleConn(ctx context.Context, conn *quic.Conn) {
	defer conn.CloseWithError(0, "")
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   g.cipher,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: noise.DHKey{Private: g.priv, Public: g.pub},
	})
	if err != nil {
		log.Printf("[mesh-agent] handshake setup: %v", err)
		return
	}

	frame, err := readMeshFrame(stream)
	if err != nil {
		log.Printf("[mesh-agent] read initiator: %v", err)
		return
	}
	if _, _, _, err := hs.ReadMessage(nil, frame); err != nil {
		log.Printf("[mesh-agent] noise read: %v", err)
		return
	}
	resp, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		log.Printf("[mesh-agent] noise write: %v", err)
		return
	}
	if err := writeMeshFrame(stream, resp); err != nil {
		return
	}

	remote := hs.PeerStatic()
	g.mu.Lock()
	g.peers++
	n := g.peers
	g.mu.Unlock()
	if len(remote) >= 8 {
		log.Printf("[mesh-agent] peer connected %x… peers=%d from %s", remote[:8], n, conn.RemoteAddr())
	} else {
		log.Printf("[mesh-agent] peer connected peers=%d from %s", n, conn.RemoteAddr())
	}

	// Keep session open; drain until peer closes (SSH/RPC frames optional later).
	buf := make([]byte, 4096)
	for {
		_ = stream.SetReadDeadline(time.Now().Add(5 * time.Minute))
		if _, err := stream.Read(buf); err != nil {
			break
		}
	}
	g.mu.Lock()
	g.peers--
	g.mu.Unlock()
	log.Printf("[mesh-agent] peer disconnected from %s", conn.RemoteAddr())
}
