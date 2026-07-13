package main

import (
	"net/http"
	"testing"
	"time"
)

func TestIsStreamingUpgrade(t *testing.T) {
	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{
			name: "websocket",
			req:  &http.Request{Header: http.Header{"Upgrade": []string{"websocket"}}},
			want: true,
		},
		{
			name: "sse",
			req:  &http.Request{Header: http.Header{"Accept": []string{"text/event-stream"}}},
			want: true,
		},
		{
			name: "plain",
			req:  &http.Request{Header: http.Header{"Accept": []string{"text/html"}}},
			want: false,
		},
	}

	for _, tc := range cases {
		if got := isStreamingUpgrade(tc.req); got != tc.want {
			t.Fatalf("%s: isStreamingUpgrade() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStreamBufferSize(t *testing.T) {
	*streamBuffer = 1024
	if got := streamBufferSize(); got != 32*1024 {
		t.Fatalf("streamBufferSize() = %d, want clamped minimum", got)
	}

	*streamBuffer = maxStreamBuffer + 1
	if got := streamBufferSize(); got != maxStreamBuffer {
		t.Fatalf("streamBufferSize() = %d, want max %d", got, maxStreamBuffer)
	}
}

func TestClientBackoff(t *testing.T) {
	if clientBackoff(0) != 2*time.Second {
		t.Fatal("expected 2s backoff for attempt 0")
	}
	if clientBackoff(10) != 60*time.Second {
		t.Fatal("expected capped 60s backoff")
	}
}
