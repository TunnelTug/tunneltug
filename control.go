package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
)

const controlALPN = "tunneltug"

type quicControlConn struct {
	*quic.Stream
	conn *quic.Conn
}

func (c *quicControlConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *quicControlConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *quicControlConn) Close() error {
	_ = c.Stream.Close()
	return c.conn.CloseWithError(0, "")
}

func controlQUICTLSConfig(base *tls.Config) *tls.Config {
	cfg := base.Clone()
	cfg.NextProtos = []string{controlALPN}
	return cfg
}

func clientQUICTLSConfig() *tls.Config {
	cfg := clientTLSConfig()
	cfg.NextProtos = []string{controlALPN}
	return cfg
}

func controlQUICConfig() *quic.Config {
	buf := uint64(streamBufferSize())
	return &quic.Config{
		KeepAlivePeriod:            time.Duration(*keepAlive) * time.Second,
		MaxIdleTimeout:             24 * time.Hour,
		InitialStreamReceiveWindow: buf,
		MaxStreamReceiveWindow:     16 * 1024 * 1024,
		MaxConnectionReceiveWindow: 32 * 1024 * 1024,
	}
}

func listenControlQUIC(addr string, tlsConfig *tls.Config) (*quic.Listener, error) {
	return quic.ListenAddr(addr, controlQUICTLSConfig(tlsConfig), controlQUICConfig())
}

func backendQUICTLSConfig() *tls.Config {
	cfg := backendTLSConfig()
	cfg.NextProtos = []string{controlALPN}
	return cfg
}

func dialControlQUIC(ctx context.Context, addr string) (net.Conn, error) {
	return dialControlQUICWithTLS(ctx, addr, clientQUICTLSConfig())
}

func dialBackendControlQUIC(ctx context.Context, addr string) (net.Conn, error) {
	return dialControlQUICWithTLS(ctx, addr, backendQUICTLSConfig())
}

func dialControlQUICWithTLS(ctx context.Context, addr string, tlsConfig *tls.Config) (net.Conn, error) {
	conn, err := quic.DialAddr(ctx, addr, tlsConfig, controlQUICConfig())
	if err != nil {
		return nil, err
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}

	return &quicControlConn{Stream: stream, conn: conn}, nil
}

func acceptControlQUICConn(ctx context.Context, conn *quic.Conn) (net.Conn, error) {
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		_ = conn.CloseWithError(0, "")
		return nil, err
	}
	return &quicControlConn{Stream: stream, conn: conn}, nil
}

func ensureControlQUIC() {
	if strings.ToLower(*protocol) != "quic" {
		log.Printf("Control channel uses QUIC; ignoring -proto %q", *protocol)
		*protocol = "quic"
	}
}
