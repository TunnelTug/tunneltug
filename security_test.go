package main

import "testing"

func TestTokensEqual(t *testing.T) {
	if !tokensEqual("abc", "abc") {
		t.Fatal("expected equal tokens to match")
	}
	if tokensEqual("abc", "abd") {
		t.Fatal("expected different tokens to not match")
	}
	if tokensEqual("abc", "abcd") {
		t.Fatal("expected different-length tokens to not match")
	}
}
