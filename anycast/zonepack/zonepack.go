// Package zonepack loads secure_dns.ZoneSnapshot files for anycast DNS bootstrap.
package zonepack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0TrustCloud/secure_dns"
)

// Pack is a loaded zone snapshot.
type Pack struct {
	Path    string
	Snap    secure_dns.ZoneSnapshot
	Records int
}

// Load reads a JSON ZoneSnapshot from path (file) or merges *.json in a directory.
// Placeholders {{VIP}} / {{ANYCAST_IP}} are replaced with vip.
func Load(path, vip string) (*Pack, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("zone_pack path is empty")
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var snap secure_dns.ZoneSnapshot
	if st.IsDir() {
		snap, err = loadDir(path, vip)
	} else {
		snap, err = loadFile(path, vip)
	}
	if err != nil {
		return nil, err
	}
	if snap.UpdatedAt.IsZero() {
		snap.UpdatedAt = time.Now().UTC()
	}
	if snap.Version == 0 {
		snap.Version = snap.UpdatedAt.UnixNano()
	}
	return &Pack{Path: path, Snap: snap, Records: len(snap.Records)}, nil
}

func loadFile(path, vip string) (secure_dns.ZoneSnapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return secure_dns.ZoneSnapshot{}, err
	}
	raw = []byte(expandVIP(string(raw), vip))
	var snap secure_dns.ZoneSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return secure_dns.ZoneSnapshot{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if len(snap.Records) == 0 {
		var recs []secure_dns.DNSRecord
		if err2 := json.Unmarshal(raw, &recs); err2 == nil && len(recs) > 0 {
			snap.Records = recs
		}
	}
	return snap, nil
}

func loadDir(dir, vip string) (secure_dns.ZoneSnapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return secure_dns.ZoneSnapshot{}, err
	}
	var merged secure_dns.ZoneSnapshot
	suffixSeen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		part, err := loadFile(filepath.Join(dir, e.Name()), vip)
		if err != nil {
			return secure_dns.ZoneSnapshot{}, err
		}
		if merged.Host == "" {
			merged.Host = part.Host
		}
		merged.Records = append(merged.Records, part.Records...)
		for _, s := range part.PrivateSuffixes {
			s = normalizeSuffix(s)
			if s == "" || suffixSeen[s] {
				continue
			}
			suffixSeen[s] = true
			merged.PrivateSuffixes = append(merged.PrivateSuffixes, s)
		}
	}
	if len(merged.Records) == 0 {
		return secure_dns.ZoneSnapshot{}, fmt.Errorf("zone_pack dir %s: no records", dir)
	}
	return merged, nil
}

func expandVIP(s, vip string) string {
	vip = strings.TrimSpace(vip)
	if vip == "" {
		return s
	}
	s = strings.ReplaceAll(s, "{{VIP}}", vip)
	s = strings.ReplaceAll(s, "{{ANYCAST_IP}}", vip)
	s = strings.ReplaceAll(s, "{{vip}}", vip)
	return s
}

func normalizeSuffix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, ".") {
		s = "." + s
	}
	return s
}
