package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenBytes is the entropy of every opaque token: 32 bytes = 256 bits from crypto/rand. That
// is far beyond guessable, which is why a fast hash (SHA-256) is the right way to store them.
const tokenBytes = 32

// NewToken returns a fresh random token and the value to store in the database.
//
// The token is what goes to the client (a session cookie, an emailed link). The database keeps
// only HashToken(token). Better Auth stores the raw session token; we deliberately do not: if
// the database leaks (backup, SQL injection, a curious DBA), raw tokens would be live logins for
// everybody, while SHA-256 digests cannot be turned back into a working cookie.
//
// Why SHA-256 and not Argon2id here? Passwords are low-entropy human choices, so the hash must
// be slow to make guessing expensive. A 256-bit random token cannot be guessed at all, so a
// fast hash is enough, and it keeps the per-request session lookup cheap (it runs on EVERY
// authenticated request).
func NewToken() (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("auth: generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b) // URL- and cookie-safe: A-Z a-z 0-9 - _
	return token, HashToken(token), nil
}

// HashToken returns the lowercase-hex SHA-256 of token. This is the value stored in (and looked
// up from) session.token_hash and verification.value.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
