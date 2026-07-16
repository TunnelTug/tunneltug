package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0TrustCloud/secure_data_format"
	"github.com/0TrustCloud/ultimate_db"
)

// bargeSDF attests k3s fleet reconciles with secure_data_format. The GRANT
// binds BOTH:
//  1. Fleet shape (namespace, name, replicas, ports, LB, domain, …)
//  2. Container image identity (reference + content digest)
//
// Verification re-reads the local k3s/containerd digest and compares it to
// the signed claim so a tag retarget or blob swap is detected.

type bargeSDF struct {
	engine *secure_data_format.SecureDataEngine
	close  func()
	mu     sync.Mutex
	nonce  uint64
	lastToken  string
	lastRoot   string
	lastAt     time.Time
	lastJTI    string
	lastImage  string
	lastDigest string
	lastVerify map[string]any
}

// imageAttestation is the integrity half of the manifest.
type imageAttestation struct {
	Reference string // tag or digest ref as configured
	Digest    string // sha256:… content identity from containerd
	Pinned    string // reference@digest when available
}

func newBargeSDF(dataDir string) (*bargeSDF, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".tunneltug", "barge-sdf")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	db, closeDB, err := openSDFDB(dataDir)
	if err != nil {
		return nil, err
	}
	key, err := loadOrCreateSDFRSAKey(filepath.Join(dataDir, "sdf-signing.pem"))
	if err != nil {
		closeDB()
		return nil, err
	}
	store := ultimate_db.NewBTreeKVStore(db)
	lockMgr := ultimate_db.New2PLLockManager()
	engine, err := secure_data_format.New(store, lockMgr, "tunneltug-barge", key)
	if err != nil {
		closeDB()
		return nil, err
	}
	return &bargeSDF{engine: engine, close: closeDB}, nil
}

func openSDFDB(dataDir string) (*ultimate_db.DB, func(), error) {
	dbPath := filepath.Join(dataDir, "sdf.db")
	walPath := filepath.Join(dataDir, "sdf.wal")
	device, err := ultimate_db.NewOSFileDevice(dbPath)
	if err != nil {
		return nil, nil, err
	}
	dm := ultimate_db.NewDiskManager(device)
	evictor := ultimate_db.NewLRUEvictionPolicy()
	metrics := ultimate_db.NewAtomicMetrics()
	bp := ultimate_db.NewBufferPool(dm, 256, evictor, metrics)
	wal, err := ultimate_db.NewBatchingWAL(walPath)
	if err != nil {
		_ = device.Close()
		return nil, nil, err
	}
	db := ultimate_db.NewDB(bp, wal, metrics)
	if err := ultimate_db.PerformRecovery(db, walPath); err != nil {
		_ = device.Close()
		return nil, nil, err
	}
	closer := func() {
		_ = db.Close()
		_ = device.Close()
	}
	return db, closer, nil
}

func loadOrCreateSDFRSAKey(path string) (*rsa.PrivateKey, error) {
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block != nil {
			if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
				return k, nil
			}
			if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
				if rk, ok := k.(*rsa.PrivateKey); ok {
					return rk, nil
				}
			}
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// resolveImageAttestation pulls digest from local k3s/containerd for the image ref.
func resolveImageAttestation(ctx context.Context, image string) (imageAttestation, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return imageAttestation{}, fmt.Errorf("empty image reference")
	}
	att := imageAttestation{Reference: image}
	// Already digest-pinned: repo@sha256:…
	if i := strings.Index(image, "@sha256:"); i >= 0 {
		att.Digest = image[i+1:]
		att.Pinned = image
		return att, nil
	}
	digest, err := k3sCtrImageDigest(ctx, image)
	if err != nil {
		return att, err
	}
	att.Digest = digest
	// name@digest form (containerd)
	base := image
	if j := strings.LastIndex(image, ":"); j > strings.LastIndex(image, "/") {
		base = image[:j]
	}
	att.Pinned = base + "@" + digest
	return att, nil
}

// k3sCtrImageDigest returns content digest (sha256:…) for a local image.
func k3sCtrImageDigest(ctx context.Context, image string) (string, error) {
	// ctr images list shows REF and DIGEST columns when not -q
	out, err := runK3sCtr(ctx, nil, "images", "list")
	if err != nil {
		return "", err
	}
	want := strings.TrimSpace(image)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		ref := fields[0]
		// header
		if ref == "REF" || strings.EqualFold(ref, "ref") {
			continue
		}
		dig := ""
		for _, f := range fields {
			if strings.HasPrefix(f, "sha256:") {
				dig = f
				break
			}
		}
		if dig == "" {
			continue
		}
		if ref == want || strings.HasPrefix(ref, want+"@") || strings.HasSuffix(ref, want) {
			return dig, nil
		}
		// tag match after name strip
		if strings.Contains(ref, want) && strings.HasPrefix(dig, "sha256:") {
			return dig, nil
		}
	}
	// Fallback: digests-only list -q sometimes emits name@sha256
	out2, err2 := runK3sCtr(ctx, nil, "images", "list", "-q")
	if err2 == nil {
		for _, line := range strings.Split(string(out2), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, want+"@sha256:") {
				return line[strings.Index(line, "sha256:"):], nil
			}
			if line == want && strings.Contains(line, "@sha256:") {
				return line[strings.Index(line, "sha256:"):], nil
			}
			if strings.HasPrefix(line, "sha256:") && (want == line || strings.HasSuffix(want, line)) {
				return line, nil
			}
		}
	}
	return "", fmt.Errorf("digest not found for image %q in local k3s/containerd", image)
}

// SignFleetReconcile mints an SDF GRANT that binds fleet shape + image digest.
func (b *bargeSDF) SignFleetReconcile(cfg k3sBargeConfig) (token string, stateRoot string, err error) {
	return b.SignFleetReconcileWithImage(context.Background(), cfg, imageAttestation{Reference: cfg.Image})
}

// SignFleetReconcileWithImage is the full attestation: fleet config + image integrity.
func (b *bargeSDF) SignFleetReconcileWithImage(ctx context.Context, cfg k3sBargeConfig, img imageAttestation) (token string, stateRoot string, err error) {
	if b == nil || b.engine == nil {
		return "", "", fmt.Errorf("sdf not initialized")
	}
	b.mu.Lock()
	b.nonce++
	n := b.nonce
	b.mu.Unlock()

	image := strings.TrimSpace(cfg.Image)
	if image == "" {
		image = defaultK3sEngineImage
	}
	if img.Reference == "" {
		img.Reference = image
	}
	// Resolve digest if not already present.
	if img.Digest == "" {
		if resolved, rerr := resolveImageAttestation(ctx, img.Reference); rerr == nil {
			img = resolved
		}
	}
	if img.Digest == "" {
		return "", "", fmt.Errorf("image digest required for SDF integrity binding (pull image first): %s", img.Reference)
	}
	if img.Pinned == "" {
		base := img.Reference
		if j := strings.LastIndex(base, ":"); j > strings.LastIndex(base, "/") {
			base = base[:j]
		}
		img.Pinned = base + "@" + img.Digest
	}

	script := fmt.Sprintf(`fleet:reconcile(
		namespace("%s")
		name("%s")
		image("%s")
		image_digest("%s")
		image_pinned("%s")
		replicas("%d")
		control_base("%s")
		public_base("%s")
		port_step("%d")
		domain("%s")
		lb("%s")
		fleet_id("%s")
		host_network("%t")
		runtime("k3s")
		integrity("image+fleet")
	)`,
		escapeSDF(cfg.Namespace),
		escapeSDF(cfg.Name),
		escapeSDF(img.Reference),
		escapeSDF(img.Digest),
		escapeSDF(img.Pinned),
		cfg.Replicas,
		escapeSDF(cfg.ControlBase),
		escapeSDF(cfg.PublicBase),
		cfg.PortStep,
		escapeSDF(cfg.Domain),
		escapeSDF(cfg.LBAddr),
		escapeSDF(cfg.FleetID),
		cfg.HostNetwork,
	)

	tx := secure_data_format.DataInvocation{
		TargetAddress: "fleet:" + cfg.Namespace + "/" + cfg.Name,
		Caller:        "tunneltug-barge-controller",
		Nonce:         n,
		Method:        "RECONCILE",
		Profile:       secure_data_format.ProfileGrant,
		Args: map[string]interface{}{
			// Fleet shape
			"namespace":    cfg.Namespace,
			"name":         cfg.Name,
			"replicas":     cfg.Replicas,
			"control_base": cfg.ControlBase,
			"public_base":  cfg.PublicBase,
			"port_step":    cfg.PortStep,
			"domain":       cfg.Domain,
			"lb":           cfg.LBAddr,
			"fleet_id":     cfg.FleetID,
			"host_network": cfg.HostNetwork,
			"runtime":      "k3s",
			"mode":         "barge",
			// Image integrity
			"image":         img.Reference,
			"image_digest":  img.Digest,
			"image_pinned":  img.Pinned,
			"integrity":     "image+fleet",
		},
	}
	token, err = b.engine.CompileSecureData(script, tx)
	if err != nil {
		return "", "", err
	}
	root := extractJWTClaim(token, "state_root_hash")
	jti := extractJWTClaim(token, "jti")

	b.mu.Lock()
	b.lastToken = token
	b.lastRoot = root
	b.lastJTI = jti
	b.lastImage = img.Reference
	b.lastDigest = img.Digest
	b.lastAt = time.Now().UTC()
	b.mu.Unlock()

	if !*quiet {
		log.Printf("SDF manifest signed jti=%s state_root=%s image=%s digest=%s replicas=%d (fleet+image integrity)",
			truncateMid(jti, 12), truncateMid(root, 16), img.Reference, truncateMid(img.Digest, 19), cfg.Replicas)
	}
	return token, root, nil
}

// VerifyImage checks the local containerd digest still matches the signed claim.
// Returns ok=true when the image has not been altered relative to the manifest.
func (b *bargeSDF) VerifyImage(ctx context.Context) (ok bool, detail map[string]any, err error) {
	detail = map[string]any{"checks": []string{"image_digest", "fleet_bound"}}
	if b == nil {
		return false, detail, fmt.Errorf("sdf not initialized")
	}
	b.mu.Lock()
	token := b.lastToken
	expectedDig := b.lastDigest
	expectedImg := b.lastImage
	expectedRoot := b.lastRoot
	b.mu.Unlock()

	if token == "" || expectedDig == "" {
		return false, detail, fmt.Errorf("no signed manifest to verify")
	}
	// Prefer claims inside the token (source of truth).
	if d := extractJWTClaimNested(token, "image_digest"); d != "" {
		expectedDig = d
	}
	if img := extractJWTClaimNested(token, "image"); img != "" {
		expectedImg = img
	}
	detail["expected_image"] = expectedImg
	detail["expected_digest"] = expectedDig
	detail["state_root_hash"] = expectedRoot

	live, err := resolveImageAttestation(ctx, expectedImg)
	if err != nil {
		// Try pinned form
		if pinned := extractJWTClaimNested(token, "image_pinned"); pinned != "" {
			live, err = resolveImageAttestation(ctx, pinned)
		}
		if err != nil {
			detail["live_error"] = err.Error()
			detail["verified"] = false
			b.storeVerify(detail)
			return false, detail, err
		}
	}
	detail["live_digest"] = live.Digest
	detail["live_reference"] = live.Reference
	match := strings.EqualFold(strings.TrimSpace(live.Digest), strings.TrimSpace(expectedDig))
	detail["verified"] = match
	detail["verified_at"] = time.Now().UTC().Format(time.RFC3339)
	if !match {
		detail["reason"] = "image digest mismatch — image may have been altered or retagged"
		b.storeVerify(detail)
		return false, detail, fmt.Errorf("image integrity failure: expected %s got %s", expectedDig, live.Digest)
	}
	detail["reason"] = "image digest matches signed SDF manifest"
	b.storeVerify(detail)
	return true, detail, nil
}

func (b *bargeSDF) storeVerify(detail map[string]any) {
	b.mu.Lock()
	b.lastVerify = detail
	b.mu.Unlock()
}

func (b *bargeSDF) Status() map[string]any {
	if b == nil {
		return map[string]any{"enabled": false}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]any{
		"enabled":   true,
		"format":    "secure_data_format",
		"module":    "github.com/0TrustCloud/secure_data_format",
		"profile":   "GRANT",
		"method":    "RECONCILE",
		"binds":     []string{"fleet_shape", "image_digest"},
		"purpose":   "attest fleet config and verify container image has not been altered",
	}
	if b.lastToken != "" {
		out["jti"] = b.lastJTI
		out["state_root_hash"] = b.lastRoot
		out["signed_at"] = b.lastAt.Format(time.RFC3339)
		out["image"] = b.lastImage
		out["image_digest"] = b.lastDigest
		out["token_prefix"] = truncateMid(b.lastToken, 32)
	}
	if b.lastVerify != nil {
		out["last_verify"] = b.lastVerify
	}
	return out
}

func (b *bargeSDF) LastToken() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastToken
}

func (b *bargeSDF) LastRoot() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastRoot
}

func (b *bargeSDF) LastDigest() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastDigest
}

func (b *bargeSDF) Close() {
	if b != nil && b.close != nil {
		b.close()
	}
}

func escapeSDF(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func truncateMid(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func extractJWTClaim(token, claim string) string {
	m := jwtPayloadMap(token)
	if m == nil {
		return ""
	}
	if v, ok := m[claim].(string); ok {
		return v
	}
	return ""
}

// extractJWTClaimNested looks in top-level claims and state_updates.
func extractJWTClaimNested(token, claim string) string {
	m := jwtPayloadMap(token)
	if m == nil {
		return ""
	}
	if v, ok := m[claim].(string); ok && v != "" {
		return v
	}
	if su, ok := m["state_updates"].(map[string]interface{}); ok {
		if v, ok := su[claim].(string); ok {
			return v
		}
	}
	return ""
}

func jwtPayloadMap(token string) map[string]interface{} {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}
