package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
)

// Cryptographic tunnel tokens: 32 random bytes → 64 hex chars (256-bit).
const secureTokenBytes = 32

// Minimum lengths (prod is stricter).
const (
	minTokenLength     = 16
	minProdTokenLength = 32
)

// Known weak defaults that must never be used.
var weakTokens = map[string]struct{}{
	"secret123":               {},
	"changeme":                {},
	"password":                {},
	"token":                   {},
	"changeme-dev-token-16":   {},
	"0trust-tunnel-prod-secret": {},
}

// GenerateSecureToken returns a cryptographically secure random token (hex).
// Callers must never accept user-submitted secrets in place of this.
func GenerateSecureToken() (string, error) {
	buf := make([]byte, secureTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func isWeakToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return true
	}
	if _, ok := weakTokens[token]; ok {
		return true
	}
	// Reject short alphabetic-only placeholders.
	if len(token) < minTokenLength {
		return true
	}
	return false
}

// ensureAuthToken validates or mints the process auth token.
// Tokens are never taken from interactive user form input in the product site;
// operators may set -token / TUNNELTUG_TOKEN or leave empty for auto-mint (non-prod).
func ensureAuthToken() error {
	token := strings.TrimSpace(*authToken)
	if token == "" || token == defaultWeakToken {
		if *prod {
			return fmt.Errorf("authentication token is required in -prod (set -token or TUNNELTUG_TOKEN to a crypto-random secret; use -gen-token to mint one)")
		}
		// Development: mint a secure token so processes never run on secret123.
		tok, err := GenerateSecureToken()
		if err != nil {
			return err
		}
		*authToken = tok
		log.Printf("Generated cryptographic tunnel token (copy for clients): %s", tok)
		log.Printf("Tip: persist with TUNNELTUG_TOKEN or -token; mint offline with -gen-token")
		return nil
	}
	if isWeakToken(token) {
		return fmt.Errorf("token is too weak or a known default; use a cryptographically random secret (openssl rand -hex 32 or -gen-token)")
	}
	minLen := minTokenLength
	if *prod {
		minLen = minProdTokenLength
	}
	if len(token) < minLen {
		return fmt.Errorf("token must be at least %d characters", minLen)
	}
	return nil
}

func maybeGenToken() bool {
	if !*genToken {
		return false
	}
	tok, err := GenerateSecureToken()
	if err != nil {
		log.Fatalf("token generation failed: %v", err)
	}
	fmt.Println(tok)
	return true
}
