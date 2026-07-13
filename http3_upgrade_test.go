package main

import (
	"net/http"
	"net/url"
	"testing"
)

func TestIsHTTP3WebSocketUpgrade(t *testing.T) {
	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{
			name: "extended connect",
			req:  &http.Request{Method: http.MethodConnect, Proto: "websocket", ProtoMajor: 3},
			want: true,
		},
		{
			name: "h3 upgrade header",
			req:  &http.Request{ProtoMajor: 3, Header: http.Header{"Upgrade": []string{"websocket"}}},
			want: true,
		},
		{
			name: "https websocket",
			req:  &http.Request{ProtoMajor: 1, Header: http.Header{"Upgrade": []string{"websocket"}}},
			want: false,
		},
		{
			name: "h3 sse",
			req:  &http.Request{ProtoMajor: 3, Header: http.Header{"Accept": []string{"text/event-stream"}}},
			want: false,
		},
	}

	for _, tc := range cases {
		if got := isHTTP3WebSocketUpgrade(tc.req); got != tc.want {
			t.Fatalf("%s: isHTTP3WebSocketUpgrade() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestH3ConnectToWebSocketRequest(t *testing.T) {
	req := &http.Request{
		Method:     http.MethodConnect,
		Proto:      "websocket",
		ProtoMajor: 3,
		Host:       "myapp.example.com",
		Header: http.Header{
			"Sec-WebSocket-Key": []string{"dGhlIHNhbXBsZSBub25jZQ=="},
		},
	}
	req.URL = &url.URL{Scheme: "https", Host: "myapp.example.com", Path: "/ws"}

	out := h3ConnectToWebSocketRequest(req)
	if out.Method != http.MethodGet {
		t.Fatalf("method = %s, want GET", out.Method)
	}
	if out.ProtoMajor != 1 || out.Header.Get("Upgrade") != "websocket" {
		t.Fatalf("unexpected converted request: %+v", out)
	}
}