package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// s3HubStore persists registry blobs on the 0trust.social S3-compatible CDN.
// Public GET/HEAD for pulls; PUT uses the cryptographic tunnel token as Bearer.
type s3HubStore struct {
	origin string
	bucket string
	token  string
	http   *http.Client
}

func newS3HubStore(origin, bucket, token string) *s3HubStore {
	return &s3HubStore{
		origin: strings.TrimRight(origin, "/"),
		bucket: strings.Trim(bucket, "/"),
		token:  token,
		http:   &http.Client{Timeout: 10 * time.Minute},
	}
}

func (s *s3HubStore) objectURL(key string) string {
	key = strings.TrimPrefix(key, "/")
	// Path-style: /s3/{bucket}/{key}
	return s.origin + "/s3/" + s.bucket + "/" + strings.TrimPrefix(key, "/")
}

func (s *s3HubStore) Put(key string, data []byte, contentType string) error {
	req, err := http.NewRequest(http.MethodPut, s.objectURL(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(data)))
	// Authenticated write to 0trust.social S3 (Bearer = crypto tunnel token).
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	// S2S to 0trust.social: same contract as product_otrust / socialcdn proxies.
	// Edge terminates TLS so social sees loopback/trusted; APISession honors
	// X-0Trust-Subject when X-0Trust-DBSC=bound (no browser cookie required).
	req.Header.Set("X-0Trust-Consumer", "tunneltug-hub")
	req.Header.Set("X-0Trust-Subject", "tunneltug-hub")
	req.Header.Set("X-0Trust-DBSC", "bound")
	req.Header.Set("X-Requested-With", "fetch")
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("s3 put %s: %s %s", key, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *s3HubStore) Get(key string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, s.objectURL(key), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", fmt.Errorf("not found")
	}
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("s3 get %s: %s", key, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func (s *s3HubStore) Head(key string) (int64, string, error) {
	req, err := http.NewRequest(http.MethodHead, s.objectURL(key), nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("not found")
	}
	return resp.ContentLength, resp.Header.Get("Content-Type"), nil
}

func (s *s3HubStore) ListPrefix(prefix string) ([]string, error) {
	// 0trust.social list: GET /s3/{bucket}?prefix=
	u, err := url.Parse(s.origin + "/s3/" + s.bucket)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("prefix", prefix)
	q.Set("max-keys", "1000")
	u.RawQuery = q.Encode()
	resp, err := s.http.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list failed: %s", resp.Status)
	}
	// Response is JSON with contents[].key — soft-parse without hard dependency.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	// Minimal extraction of "key":"..." fields under contents.
	var keys []string
	const marker = `"key"`
	sraw := string(raw)
	for i := 0; i < len(sraw); {
		j := strings.Index(sraw[i:], marker)
		if j < 0 {
			break
		}
		i += j + len(marker)
		// skip : and whitespace/quotes
		for i < len(sraw) && (sraw[i] == ' ' || sraw[i] == ':' || sraw[i] == '"') {
			if sraw[i] == '"' {
				i++
				break
			}
			i++
		}
		start := i
		for i < len(sraw) && sraw[i] != '"' {
			i++
		}
		if i > start {
			keys = append(keys, sraw[start:i])
		}
	}
	return keys, nil
}

// memoryHubStore is for tests and local dry-run without S3.
type memoryHubStore struct {
	mu   sync.RWMutex
	data map[string][]byte
	ct   map[string]string
}

func newMemoryHubStore() *memoryHubStore {
	return &memoryHubStore{
		data: make(map[string][]byte),
		ct:   make(map[string]string),
	}
}

func (m *memoryHubStore) Put(key string, data []byte, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.data[key] = cp
	m.ct[key] = contentType
	return nil
}

func (m *memoryHubStore) Get(key string) ([]byte, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.data[key]
	if !ok {
		return nil, "", fmt.Errorf("not found")
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, m.ct[key], nil
}

func (m *memoryHubStore) Head(key string) (int64, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.data[key]
	if !ok {
		return 0, "", fmt.Errorf("not found")
	}
	return int64(len(data)), m.ct[key], nil
}

func (m *memoryHubStore) ListPrefix(prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}
