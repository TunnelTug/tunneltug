package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	defaultStreamBuffer = 256 * 1024
	maxStreamBuffer     = 4 * 1024 * 1024
)

var streamCopyPool = sync.Pool{
	New: func() any {
		buf := make([]byte, defaultStreamBuffer)
		return &buf
	},
}

func streamingYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 4096
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = time.Duration(*keepAlive) * time.Second
	cfg.ConnectionWriteTimeout = 24 * time.Hour
	cfg.StreamOpenTimeout = 0
	cfg.StreamCloseTimeout = 24 * time.Hour
	cfg.MaxStreamWindowSize = 16 * 1024 * 1024
	if *quiet {
		cfg.LogOutput = io.Discard
	}
	return cfg
}

func streamBufferSize() int {
	size := *streamBuffer
	if size < 32*1024 {
		return 32 * 1024
	}
	if size > maxStreamBuffer {
		return maxStreamBuffer
	}
	return size
}

func tuneTCPConn(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
		_ = tcp.SetReadBuffer(streamBufferSize())
		_ = tcp.SetWriteBuffer(streamBufferSize())
	}
}

func copyStream(dst, src net.Conn) (int64, error) {
	bufPtr := streamCopyPool.Get().(*[]byte)
	defer streamCopyPool.Put(bufPtr)
	return io.CopyBuffer(dst, src, *bufPtr)
}

func relayConns(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	closeBoth := func() {
		_ = a.Close()
		_ = b.Close()
	}

	go func() {
		defer wg.Done()
		_, _ = copyStream(b, a)
		closeBoth()
	}()

	go func() {
		defer wg.Done()
		_, _ = copyStream(a, b)
		closeBoth()
	}()

	wg.Wait()
}

func isStreamingUpgrade(r *http.Request) bool {
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))
	if upgrade == "websocket" || upgrade == "h2c" {
		return true
	}
	if strings.EqualFold(r.Header.Get("Connection"), "Upgrade") {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "text/event-stream") {
		return true
	}
	return false
}

func openTunnelStream(session *yamux.Session) (net.Conn, error) {
	stream, err := session.Open()
	if err != nil {
		return nil, err
	}
	if sc, ok := stream.(net.Conn); ok {
		tuneTCPConn(sc)
	}
	return stream, nil
}

func writeForwardedRequest(stream net.Conn, r *http.Request) error {
	if err := r.Write(stream); err != nil {
		return err
	}
	return nil
}

func proxyUpgrade(w http.ResponseWriter, r *http.Request, session *yamux.Session) {
	clientConn, bufrw, err := hijackResponse(w)
	if err != nil {
		http.Error(w, "upgrade proxy unavailable", http.StatusInternalServerError)
		return
	}

	stream, err := openTunnelStream(session)
	if err != nil {
		_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\nTunnel offline"))
		_ = clientConn.Close()
		return
	}

	if bufrw.Reader.Buffered() > 0 {
		stream = &bufferedConn{Conn: stream, r: bufrw.Reader}
	}

	if err := writeForwardedRequest(stream, r); err != nil {
		_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\nTunnel write failed"))
		_ = stream.Close()
		_ = clientConn.Close()
		return
	}

	tuneTCPConn(clientConn)
	relayConns(clientConn, stream)
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}

func hijackResponse(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, io.ErrUnexpectedEOF
	}
	return hj.Hijack()
}
