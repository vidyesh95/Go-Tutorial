package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// issuerName is the "iss" claim. Parse requires it, so a token minted by another service that
// happens to share the secret is still rejected.
const issuerName = "taskapi"

// MinSecretBytes mirrors config.MinSecretBytes. Duplicated deliberately: this package enforces
// the rule itself instead of trusting its caller (defense in depth).
const MinSecretBytes = 32

// ErrInvalidToken is returned for EVERY token problem (malformed, bad signature, wrong
// algorithm, expired, wrong issuer...). Callers cannot and should not distinguish them: telling
// a client precisely why a token failed only helps an attacker.
var ErrInvalidToken = errors.New("invalid token")

// Claims is what we extract from a valid token.
type Claims struct {
	UserID    string
	ExpiresAt time.Time
}

// Issuer creates and verifies HS256 JWTs.
//
// HS256 is a symmetric scheme: the same secret signs and verifies. That is right for a single
// service that is both issuer and verifier. If other services must verify tokens without being
// able to mint them, switch to an asymmetric algorithm (RS256/EdDSA) and publish the public key.
type Issuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
	parser *jwt.Parser
}

// NewIssuer builds an Issuer. now may be nil (defaults to time.Now); tests inject a fake clock
// to exercise expiry without sleeping.
func NewIssuer(secret []byte, ttl time.Duration, now func() time.Time) (*Issuer, error) {
	if len(secret) < MinSecretBytes {
		return nil, fmt.Errorf("auth: jwt secret must be at least %d bytes", MinSecretBytes)
	}
	if ttl <= 0 {
		return nil, errors.New("auth: jwt ttl must be positive")
	}
	if now == nil {
		now = time.Now
	}
	return &Issuer{
		// Copy so the caller mutating its slice later cannot change our key.
		secret: append([]byte(nil), secret...),
		ttl:    ttl,
		now:    now,
		parser: jwt.NewParser(
			// THE classic JWT vulnerability: a verifier that trusts the token's own "alg" header
			// can be tricked into "none" (no signature) or into treating an RSA public key as an
			// HMAC secret. Pinning the accepted algorithm closes both.
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(issuerName),
			// Without this, a token with no "exp" claim would be valid forever.
			jwt.WithExpirationRequired(),
			jwt.WithTimeFunc(now),
		),
	}, nil
}

// Issue signs a token for userID and returns it with its expiry time.
func (i *Issuer) Issue(userID string) (string, time.Time, error) {
	now := i.now()
	exp := now.Add(i.ttl)
	claims := jwt.RegisteredClaims{
		Issuer:    issuerName,
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign token: %w", err)
	}
	// JWT timestamps have one-second precision; report what the token actually says.
	return signed, claims.ExpiresAt.Time, nil
}

// Parse verifies the signature, algorithm, issuer and expiry, and returns the claims.
func (i *Issuer) Parse(token string) (Claims, error) {
	var rc jwt.RegisteredClaims
	_, err := i.parser.ParseWithClaims(token, &rc, func(*jwt.Token) (any, error) {
		return i.secret, nil
	})
	if err != nil {
		// %w on ErrInvalidToken only; the jwt library's reason stays out of the chain so it
		// cannot leak into a response, but it is still in the string for server-side logs.
		return Claims{}, fmt.Errorf("%w: %s", ErrInvalidToken, err)
	}
	if rc.Subject == "" {
		return Claims{}, fmt.Errorf("%w: missing subject", ErrInvalidToken)
	}
	return Claims{UserID: rc.Subject, ExpiresAt: rc.ExpiresAt.Time}, nil
}
