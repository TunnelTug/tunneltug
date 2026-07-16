// Package rov implements RPKI Route Origin Validation (RFC 6811 / RFC 6483)
// against statically configured or file-loaded ROAs. TunnelTug uses this as a
// fail-closed gate: we only announce prefixes our origin ASN is authorized to
// originate (so peer ROV / RPKI accepts the route).
package rov

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
)

// Validity is the RFC 6811 validation state.
type Validity int

const (
	Valid Validity = iota
	Invalid
	NotFound
)

func (v Validity) String() string {
	switch v {
	case Valid:
		return "valid"
	case Invalid:
		return "invalid"
	default:
		return "not_found"
	}
}

// ROA is one Route Origin Authorization covering a prefix for an ASN.
type ROA struct {
	Prefix    string `yaml:"prefix" json:"prefix"`
	ASN       uint32 `yaml:"asn" json:"asn"`
	MaxLength int    `yaml:"max_length" json:"max_length"`
	// Parsed network (filled by Compile).
	ipNet *net.IPNet
	ones  int
	bits  int
}

// Validator holds compiled ROAs for origin checks.
type Validator struct {
	enabled       bool
	roas          []ROA
	requireValid  bool // if true, NotFound is treated as fail
	allowPrivate  bool // if true, RFC 6996 private ASN + docs prefixes skip ROA
}

// Config for building a Validator.
type Config struct {
	Enabled      bool
	RequireValid bool
	AllowPrivate bool
	ROAs         []ROA
	ROAFile      string
}

// New builds a Validator. When Enabled is false, Validate always returns Valid.
func New(cfg Config) (*Validator, error) {
	v := &Validator{
		enabled:      cfg.Enabled,
		requireValid: cfg.RequireValid,
		allowPrivate: cfg.AllowPrivate,
	}
	if !cfg.Enabled {
		return v, nil
	}
	roas := append([]ROA{}, cfg.ROAs...)
	if path := strings.TrimSpace(cfg.ROAFile); path != "" {
		extra, err := LoadROAFile(path)
		if err != nil {
			return nil, err
		}
		roas = append(roas, extra...)
	}
	compiled, err := compileROAs(roas)
	if err != nil {
		return nil, err
	}
	v.roas = compiled
	if len(v.roas) == 0 && cfg.RequireValid && !cfg.AllowPrivate {
		return nil, fmt.Errorf("rov: require_valid set but no ROAs configured (or set allow_private for lab)")
	}
	return v, nil
}

// LoadROAFile reads a JSON array of {prefix, asn, max_length} (rpki-client style subset).
func LoadROAFile(path string) ([]ROA, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("roa file: %w", err)
	}
	var list []ROA
	if err := json.Unmarshal(raw, &list); err != nil {
		// Try wrapped form {"roas":[...]}
		var wrap struct {
			ROAs []ROA `json:"roas"`
		}
		if err2 := json.Unmarshal(raw, &wrap); err2 != nil {
			return nil, fmt.Errorf("roa file json: %w", err)
		}
		list = wrap.ROAs
	}
	return list, nil
}

func compileROAs(in []ROA) ([]ROA, error) {
	out := make([]ROA, 0, len(in))
	for i, r := range in {
		p := strings.TrimSpace(r.Prefix)
		if p == "" {
			return nil, fmt.Errorf("roa[%d]: empty prefix", i)
		}
		_, ipNet, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("roa[%d] prefix %q: %w", i, p, err)
		}
		ones, bits := ipNet.Mask.Size()
		maxLen := r.MaxLength
		if maxLen == 0 {
			maxLen = ones
		}
		if maxLen < ones || maxLen > bits {
			return nil, fmt.Errorf("roa[%d]: max_length %d invalid for %s", i, maxLen, p)
		}
		if r.ASN == 0 {
			return nil, fmt.Errorf("roa[%d]: asn required", i)
		}
		out = append(out, ROA{
			Prefix:    p,
			ASN:       r.ASN,
			MaxLength: maxLen,
			ipNet:     ipNet,
			ones:      ones,
			bits:      bits,
		})
	}
	return out, nil
}

// ValidateOrigin checks whether originASN may announce prefix (RFC 6811).
func (v *Validator) ValidateOrigin(prefix string, originASN uint32) (Validity, string) {
	if v == nil || !v.enabled {
		return Valid, "rov disabled"
	}
	if len(v.roas) == 0 {
		if v.allowPrivate && isDocumentationOrPrivate(prefix, originASN) {
			return Valid, "private/documentation space (allow_private)"
		}
		if !v.requireValid {
			return Valid, "rov enabled, empty table (not require_valid)"
		}
		return NotFound, "no covering ROA"
	}

	if v.allowPrivate && isDocumentationOrPrivate(prefix, originASN) {
		return Valid, "private/documentation space (allow_private)"
	}

	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(prefix))
	if err != nil {
		return Invalid, err.Error()
	}
	ones, _ := ipNet.Mask.Size()
	// Use network address for containment checks.
	_ = ip
	network := ipNet.IP.Mask(ipNet.Mask)

	var coveringWithOtherASN bool
	for _, roa := range v.roas {
		if !roa.ipNet.Contains(network) {
			// Also require announced prefix is equal or more specific than ROA.
			continue
		}
		// Announced prefix length must be >= ROA prefix length and <= maxLength.
		roaOnes, _ := roa.ipNet.Mask.Size()
		// network must be within ROA prefix: check ROA contains network and announced is more specific or equal.
		if ones < roaOnes || ones > roa.MaxLength {
			continue
		}
		// Ensure announced network is subnet of ROA (not just Contains of one host).
		if !roa.ipNet.Contains(network) {
			continue
		}
		if roa.ASN == originASN {
			return Valid, fmt.Sprintf("ROA %s max %d asn %d", roa.Prefix, roa.MaxLength, roa.ASN)
		}
		coveringWithOtherASN = true
	}
	if coveringWithOtherASN {
		return Invalid, "covering ROA exists for different ASN"
	}
	return NotFound, "no covering ROA"
}

// AllowAnnounce returns true if this origin may be announced under policy.
func (v *Validator) AllowAnnounce(prefix string, originASN uint32) (bool, Validity, string) {
	val, detail := v.ValidateOrigin(prefix, originASN)
	switch val {
	case Valid:
		return true, val, detail
	case Invalid:
		return false, val, detail
	default: // NotFound
		if v != nil && v.requireValid {
			return false, val, detail
		}
		return true, val, detail
	}
}

// Status for HTTP API.
func (v *Validator) Status() map[string]any {
	if v == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":       v.enabled,
		"roa_count":     len(v.roas),
		"require_valid": v.requireValid,
		"allow_private": v.allowPrivate,
	}
}

func isDocumentationOrPrivate(prefix string, asn uint32) bool {
	// Private ASNs (RFC 6996) and documentation prefixes (RFC 5737 / 3849).
	if asn >= 64512 && asn <= 65534 {
		return true
	}
	if asn >= 4200000000 && asn <= 4294967294 {
		return true
	}
	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(prefix))
	if err != nil {
		return false
	}
	_ = ipNet
	if v4 := ip.To4(); v4 != nil {
		// 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24
		if v4[0] == 192 && v4[1] == 0 && v4[2] == 2 {
			return true
		}
		if v4[0] == 198 && v4[1] == 51 && v4[2] == 100 {
			return true
		}
		if v4[0] == 203 && v4[1] == 0 && v4[2] == 113 {
			return true
		}
		// RFC 1918
		if v4[0] == 10 {
			return true
		}
		if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
			return true
		}
		if v4[0] == 192 && v4[1] == 168 {
			return true
		}
	}
	return false
}
