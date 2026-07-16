// Package bgpsec implements BGPsec origin signing (RFC 8205 / RFC 8208)
// for TunnelTug anycast announcements. Signatures are produced in-process
// with the operator's RPKI router private key (ECDSA P-256, algorithm suite 1).
package bgpsec

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
)

// Algorithm suite identifier for SHA-256 / ECDSA P-256 (RFC 8208).
const AlgorithmSuite1 uint8 = 1

// OriginSignature is a BGPsec origin Signature Segment + Secure_Path context.
type OriginSignature struct {
	// TargetASN is the eBGP peer AS the signature is bound to (RFC 8205 §4.2).
	TargetASN uint32
	// OriginASN is the AS inserting Secure_Path Segment N=1.
	OriginASN uint32
	// PCount for the Secure_Path Segment (normally 1).
	PCount uint8
	// Flags: Confed_Segment bit and reserved (normally 0).
	Flags uint8
	// AlgorithmSuite is 1 for suite SHA-256/ECDSA-P-256.
	AlgorithmSuite uint8
	// AFI / SAFI / NLRI covered by the signature.
	AFI  uint16
	SAFI uint8
	// Prefix is the CIDR being originated.
	Prefix string
	// SKI is the 20-octet Subject Key Identifier from the router certificate.
	SKI []byte
	// Signature is r||s (64 octets) for algorithm suite 1.
	Signature []byte
	// HashInput is the exact octet sequence hashed (for debugging / export).
	HashInput []byte
	// BGPsecPathWire is a compact encoding of Secure_Path + one Signature_Block
	// suitable for tooling / file backends (not a full BGP UPDATE).
	BGPsecPathWire []byte
}

// Signer holds the ECDSA P-256 private key and SKI used to sign originated routes.
type Signer struct {
	key *ecdsa.PrivateKey
	ski [20]byte
	asn uint32
}

// LoadSigner loads a PEM-encoded ECDSA P-256 private key and optional SKI hex.
// If skiHex is empty, SKI is RFC 7093 method 1 style: leftmost 160 bits of SHA-256(SPKI).
func LoadSigner(privateKeyPath, skiHex string, originASN uint32) (*Signer, error) {
	if originASN == 0 {
		return nil, fmt.Errorf("bgpsec: origin ASN is required")
	}
	raw, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("bgpsec private key: %w", err)
	}
	key, err := parseECPrivateKey(raw)
	if err != nil {
		return nil, err
	}
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("bgpsec: private key must be ECDSA P-256 (RFC 8208 suite 1), got %s", key.Curve.Params().Name)
	}
	s := &Signer{key: key, asn: originASN}
	if strings.TrimSpace(skiHex) != "" {
		b, err := hex.DecodeString(strings.TrimSpace(skiHex))
		if err != nil {
			return nil, fmt.Errorf("bgpsec ski hex: %w", err)
		}
		if len(b) != 20 {
			return nil, fmt.Errorf("bgpsec ski: want 20 octets, got %d", len(b))
		}
		copy(s.ski[:], b)
	} else {
		s.ski = deriveSKI(&key.PublicKey)
	}
	return s, nil
}

// NewEphemeralSigner generates an in-memory P-256 key (tests / dry-run only).
func NewEphemeralSigner(originASN uint32) (*Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Signer{key: key, asn: originASN, ski: deriveSKI(&key.PublicKey)}, nil
}

// SKIHex returns the Subject Key Identifier as hex.
func (s *Signer) SKIHex() string {
	return hex.EncodeToString(s.ski[:])
}

// ASN returns the origin ASN configured for this signer.
func (s *Signer) ASN() uint32 { return s.asn }

// PublicKeyPEM returns the SubjectPublicKeyInfo PEM for registration with an RPKI CA.
func (s *Signer) PublicKeyPEM() (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&s.key.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// SignOrigin produces a BGPsec origin signature for one prefix toward targetASN
// (RFC 8205 §4.2, N=1 origin case).
func (s *Signer) SignOrigin(prefix string, targetASN uint32) (*OriginSignature, error) {
	if s == nil || s.key == nil {
		return nil, fmt.Errorf("bgpsec: nil signer")
	}
	if targetASN == 0 {
		return nil, fmt.Errorf("bgpsec: target ASN (peer) is required for origin signature")
	}
	afi, safi, nlri, err := encodeNLRI(prefix)
	if err != nil {
		return nil, err
	}
	pCount := uint8(1)
	flags := uint8(0)

	// Hash input (RFC 8205 Figure 8), origin N=1:
	// Target AS | Secure_Path Segment 1 | Algorithm Suite | AFI | SAFI | NLRI
	var hashIn []byte
	hashIn = appendU32(hashIn, targetASN)
	hashIn = append(hashIn, pCount, flags)
	hashIn = appendU32(hashIn, s.asn)
	hashIn = append(hashIn, AlgorithmSuite1)
	hashIn = appendU16(hashIn, afi)
	hashIn = append(hashIn, safi)
	hashIn = append(hashIn, nlri...)

	digest := sha256.Sum256(hashIn)
	r, ss, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return nil, fmt.Errorf("bgpsec sign: %w", err)
	}
	sig := encodeRS(r, ss)

	out := &OriginSignature{
		TargetASN:      targetASN,
		OriginASN:      s.asn,
		PCount:         pCount,
		Flags:          flags,
		AlgorithmSuite: AlgorithmSuite1,
		AFI:            afi,
		SAFI:           safi,
		Prefix:         prefix,
		SKI:            append([]byte{}, s.ski[:]...),
		Signature:      sig,
		HashInput:      hashIn,
	}
	out.BGPsecPathWire = encodeBGPsecPath(out)
	return out, nil
}

// VerifyOrigin checks a signature with the signer's public key (self-check).
func (s *Signer) VerifyOrigin(sig *OriginSignature) bool {
	if s == nil || sig == nil || len(sig.HashInput) == 0 || len(sig.Signature) != 64 {
		return false
	}
	digest := sha256.Sum256(sig.HashInput)
	r := new(big.Int).SetBytes(sig.Signature[:32])
	ss := new(big.Int).SetBytes(sig.Signature[32:])
	return ecdsa.Verify(&s.key.PublicKey, digest[:], r, ss)
}

// GenerateKeyPEM creates a new P-256 private key PEM (operator onboarding).
func GenerateKeyPEM() (privatePEM string, skiHex string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	ski := deriveSKI(&key.PublicKey)
	return string(pemBytes), hex.EncodeToString(ski[:]), nil
}

func parseECPrivateKey(raw []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("bgpsec: no PEM block in private key file")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("bgpsec: PKCS#8 key is not ECDSA")
		}
		return ek, nil
	default:
		// Try raw SEC1 / PKCS8 without relying on type string.
		if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
			return k, nil
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("bgpsec: unsupported key PEM type %q", block.Type)
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("bgpsec: key is not ECDSA")
		}
		return ek, nil
	}
}

// deriveSKI: SHA-256 of SubjectPublicKeyInfo, leftmost 160 bits (common RPKI practice).
func deriveSKI(pub *ecdsa.PublicKey) [20]byte {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return [20]byte{}
	}
	sum := sha256.Sum256(der)
	var ski [20]byte
	copy(ski[:], sum[:20])
	return ski
}

func encodeNLRI(prefix string) (afi uint16, safi uint8, nlri []byte, err error) {
	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(prefix))
	if err != nil {
		return 0, 0, nil, fmt.Errorf("bgpsec prefix %q: %w", prefix, err)
	}
	ones, bits := ipNet.Mask.Size()
	if ones < 0 {
		return 0, 0, nil, fmt.Errorf("bgpsec prefix %q: invalid mask", prefix)
	}
	// Trailing bits in prefix must be zero (RFC 8205).
	masked := ip.Mask(ipNet.Mask)
	if !ip.Equal(masked) && ip.To4() != nil {
		// Compare v4-mapped carefully.
		if !net.IP(masked).Equal(ip.Mask(ipNet.Mask)) {
			// re-check using network address
		}
	}
	network := ipNet.IP.Mask(ipNet.Mask)

	if v4 := network.To4(); v4 != nil && bits == 32 {
		afi, safi = 1, 1 // unicast
		byteLen := (ones + 7) / 8
		nlri = make([]byte, 1+byteLen)
		nlri[0] = byte(ones)
		copy(nlri[1:], v4[:byteLen])
		// Clear unused bits in last octet.
		if ones%8 != 0 && byteLen > 0 {
			mask := byte(0xFF << (8 - ones%8))
			nlri[byteLen] &= mask
		}
		return afi, safi, nlri, nil
	}
	if v6 := network.To16(); v6 != nil && bits == 128 && network.To4() == nil {
		afi, safi = 2, 1
		byteLen := (ones + 7) / 8
		nlri = make([]byte, 1+byteLen)
		nlri[0] = byte(ones)
		copy(nlri[1:], v6[:byteLen])
		if ones%8 != 0 && byteLen > 0 {
			mask := byte(0xFF << (8 - ones%8))
			nlri[byteLen] &= mask
		}
		return afi, safi, nlri, nil
	}
	return 0, 0, nil, fmt.Errorf("bgpsec prefix %q: unsupported address family", prefix)
}

func encodeRS(r, s *big.Int) []byte {
	out := make([]byte, 64)
	rb := r.Bytes()
	sb := s.Bytes()
	copy(out[32-len(rb):32], rb)
	copy(out[64-len(sb):64], sb)
	return out
}

func encodeBGPsecPath(sig *OriginSignature) []byte {
	// Secure_Path: length (2) + segment (6)
	// Signature_Block: length (2) + alg (1) + segment (20 + 2 + sigLen)
	securePath := make([]byte, 0, 8)
	securePath = appendU16(securePath, 8) // length including itself
	securePath = append(securePath, sig.PCount, sig.Flags)
	securePath = appendU32(securePath, sig.OriginASN)

	sigSeg := make([]byte, 0, 22+len(sig.Signature))
	sigSeg = append(sigSeg, sig.SKI...)
	sigSeg = appendU16(sigSeg, uint16(len(sig.Signature)))
	sigSeg = append(sigSeg, sig.Signature...)

	blockBody := make([]byte, 0, 1+len(sigSeg))
	blockBody = append(blockBody, sig.AlgorithmSuite)
	blockBody = append(blockBody, sigSeg...)
	block := make([]byte, 0, 2+len(blockBody))
	block = appendU16(block, uint16(2+len(blockBody)))
	block = append(block, blockBody...)

	out := make([]byte, 0, len(securePath)+len(block))
	out = append(out, securePath...)
	out = append(out, block...)
	return out
}

func appendU16(b []byte, v uint16) []byte {
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

// MarshalJSON fields for status export.
func (s *OriginSignature) Summary() map[string]any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"prefix":           s.Prefix,
		"origin_asn":       s.OriginASN,
		"target_asn":       s.TargetASN,
		"algorithm_suite":  s.AlgorithmSuite,
		"afi":              s.AFI,
		"safi":             s.SAFI,
		"ski":              hex.EncodeToString(s.SKI),
		"signature_hex":    hex.EncodeToString(s.Signature),
		"bgpsec_path_hex":  hex.EncodeToString(s.BGPsecPathWire),
		"hash_input_hex":   hex.EncodeToString(s.HashInput),
		"signature_octets": len(s.Signature),
	}
}

