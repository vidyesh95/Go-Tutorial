package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// fakeClock is a mutable time source for expiry tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestIssuer(t *testing.T, clock *fakeClock) *Issuer {
	t.Helper()
	i, err := NewIssuer(testSecret, time.Hour, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestNewIssuerValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewIssuer([]byte("short"), time.Hour, nil); err == nil {
		t.Error("short secret accepted")
	}
	if _, err := NewIssuer(testSecret, 0, nil); err == nil {
		t.Error("zero ttl accepted")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	iss := newTestIssuer(t, clock)

	tok, exp, err := iss.Issue("user-123")
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.t.Add(time.Hour); !exp.Equal(want) {
		t.Fatalf("expiry = %v, want %v", exp, want)
	}
	claims, err := iss.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "user-123" || !claims.ExpiresAt.Equal(exp) {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestTokenRejections(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	iss := newTestIssuer(t, clock)
	good, _, _ := iss.Issue("user-123")

	// sign mints a token with arbitrary method/key/claims, as an attacker might.
	sign := func(method jwt.SigningMethod, key any, c jwt.RegisteredClaims) string {
		s, err := jwt.NewWithClaims(method, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	valid := jwt.RegisteredClaims{
		Issuer: issuerName, Subject: "user-123",
		ExpiresAt: jwt.NewNumericDate(clock.t.Add(time.Hour)),
	}
	with := func(f func(*jwt.RegisteredClaims)) jwt.RegisteredClaims { c := valid; f(&c); return c }

	// Privilege escalation attempt: splice another user's (well-formed) payload onto the
	// signature of a legitimate token. The signature no longer matches the payload.
	other, _, _ := iss.Issue("user-999")
	parts, otherParts := strings.Split(good, "."), strings.Split(other, ".")
	tampered := parts[0] + "." + otherParts[1] + "." + parts[2]

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"garbage", "not.a.jwt"},
		{"tampered payload", tampered},
		{"bad signature (wrong key)", sign(jwt.SigningMethodHS256, []byte("another-secret-another-secret-xx"), valid)},
		{"alg none", sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, valid)},
		{"alg HS512 (not pinned HS256)", sign(jwt.SigningMethodHS512, testSecret, valid)},
		{"expired", sign(jwt.SigningMethodHS256, testSecret, with(func(c *jwt.RegisteredClaims) {
			c.ExpiresAt = jwt.NewNumericDate(clock.t.Add(-time.Second))
		}))},
		{"no exp claim", sign(jwt.SigningMethodHS256, testSecret, with(func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil }))},
		{"wrong issuer", sign(jwt.SigningMethodHS256, testSecret, with(func(c *jwt.RegisteredClaims) { c.Issuer = "evil" }))},
		{"no subject", sign(jwt.SigningMethodHS256, testSecret, with(func(c *jwt.RegisteredClaims) { c.Subject = "" }))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := iss.Parse(tt.token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("got %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestTokenExpiresWithClock(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	iss := newTestIssuer(t, clock)
	tok, _, _ := iss.Issue("u")

	clock.t = clock.t.Add(59 * time.Minute)
	if _, err := iss.Parse(tok); err != nil {
		t.Fatalf("token should still be valid: %v", err)
	}
	clock.t = clock.t.Add(2 * time.Minute)
	if _, err := iss.Parse(tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token should be expired, got %v", err)
	}
}
