package auth

import (
	"encoding/base64"
	"regexp"
	"testing"
)

func TestNewToken(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 100 {
		tok, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(raw) != 32 {
			t.Fatalf("token %q is not 32 bytes of base64url (err %v)", tok, err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
			t.Fatalf("hash %q is not 64 hex chars", hash)
		}
		if hash != HashToken(tok) {
			t.Fatal("NewToken hash differs from HashToken(token)")
		}
		if tok == hash || seen[tok] {
			t.Fatal("token repeated or equals its hash")
		}
		seen[tok] = true
	}
	// Known vector: SHA-256("abc").
	if got := HashToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("HashToken(abc) = %s", got)
	}
}
