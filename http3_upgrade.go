package main

import (
	"bufio"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go/http3"
)

func isHTTP3WebSocketUpgrade(r *http.Request) bool {
	if r == nil || r.ProtoMajor < 3 {
		return false
	}
	if r.Method == http.MethodConnect && strings.EqualFold(r.Proto, "websocket") {
		return true
	}
	return strings.ToLower(r.Header.Get("Upgrade")) == "websocket"
}

func h3ConnectToWebSocketRequest(r *http.Request) *http.Request {
	out := r.Clone(r.Context())
	out.Method = http.MethodGet
	out.Proto = "HTTP/1.1"
	out.ProtoMajor = 1
	out.ProtoMinor = 1
	out.RequestURI = r.URL.RequestURI()
	if out.Header.Get("Upgrade") == "" {
		out.Header.Set("Upgrade", "websocket")
	}
	if out.Header.Get("Connection") == "" {
		out.Header.Set("Connection", "Upgrade")
	}
	out.Header.Del("Content-Length")
	out.Body = http.NoBody
	out.ContentLength = 0
	return out
}

func proxyHTTP3Upgrade(w http.ResponseWriter, r *http.Request, session *yamux.Session) {
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		http.Error(w, "HTTP/3 stream takeover unavailable", http.StatusInternalServerError)
		return
	}

	tunnelStream, err := openTunnelStream(session)
	if err != nil {
		http.Error(w, "Tunnel upstream unavailable", http.StatusBadGateway)
		return
	}

	backendReq := r
	if r.Method == http.MethodConnect {
		backendReq = h3ConnectToWebSocketRequest(r)
	}
	if err := writeForwardedRequest(tunnelStream, backendReq); err != nil {
		_ = tunnelStream.Close()
		http.Error(w, "Tunnel write failed", http.StatusBadGateway)
		return
	}

	if r.Method == http.MethodConnect {
		w.WriteHeader(http.StatusOK)
	}

	h3Stream := streamer.HTTPStream()
	relayConns(&http3StreamConn{Stream: h3Stream}, tunnelStream)
}

func proxyHTTP3UpgradeOverConn(w http.ResponseWriter, r *http.Request, upstream net.Conn) {
	streamer, ok := w.(http3.HTTPStreamer)
	if !ok {
		http.Error(w, "HTTP/3 stream takeover unavailable", http.StatusInternalServerError)
		return
	}

	backendReq := r
	if r.Method == http.MethodConnect {
		backendReq = h3ConnectToWebSocketRequest(r)
	}
	if err := writeForwardedRequest(upstream, backendReq); err != nil {
		_ = upstream.Close()
		http.Error(w, "Upstream write failed", http.StatusBadGateway)
		return
	}

	if r.Method == http.MethodConnect {
		w.WriteHeader(http.StatusOK)
	}

	h3Stream := streamer.HTTPStream()
	relayConns(&http3StreamConn{Stream: h3Stream}, upstream)
}

func dialBackendUpgradeConn(backend *tunnelBackend) (net.Conn, error) {
	addr := backend.publicAddr()
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	if backend.publicScheme() == "https" {
		return tls.DialWithDialer(dialer, "tcp", addr, backendTLSConfig())
	}
	return dialer.Dial("tcp", addr)
}

type http3StreamConn struct {
	*http3.Stream
}

func (c *http3StreamConn) LocalAddr() net.Addr  { return nil }
func (c *http3StreamConn) RemoteAddr() net.Addr { return nil }

func upgradeProxyBuffered(stream net.Conn, bufrw *bufio.ReadWriter) net.Conn {
	if bufrw.Reader.Buffered() > 0 {
		return &bufferedConn{Conn: stream, r: bufrw.Reader}
	}
	return stream
}