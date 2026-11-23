// Package auth contains the security primitives of the service: password hashing (Argon2id),
// signed JWTs (stateless bearer auth), opaque random tokens (database-backed sessions, email
// verification, password reset) and the GitHub OAuth client. It knows nothing about HTTP routing
// or the database.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the tunable Argon2id cost parameters.
//
// Why Argon2id and not bcrypt? Password hashing exists to make an OFFLINE attack on a stolen
// database slow. Attackers do not use CPUs, they use GPUs and ASICs with thousands of cores.
// bcrypt needs only 4 KiB of memory, so those cores run it nearly for free. Argon2 is
// "memory-hard": every guess must fill and repeatedly read Memory KiB of RAM, and RAM per core is
// exactly what a GPU or ASIC lacks. That is what the Memory parameter buys you; Iterations then
// adds sequential time on top. (Argon2id is the hybrid variant: it resists both side-channel
// attacks, like Argon2i, and GPU/TMTO attacks, like Argon2d. It is the one RFC 9106 and OWASP
// recommend. It also has no 72-byte input limit, unlike bcrypt.)
type Params struct {
	Memory      uint32 // KiB of RAM per hash ("m")
	Iterations  uint32 // passes over that memory ("t")
	Parallelism uint8  // lanes/threads ("p")
	SaltLen     uint32 // bytes of random salt
	KeyLen      uint32 // bytes of output hash
}

// DefaultParams follows the OWASP Password Storage Cheat Sheet's first-choice Argon2id setting:
// m=19 MiB (19456 KiB), t=2, p=1. OWASP lists several equivalent trade-offs (for example m=46 MiB,
// t=1 or m=12 MiB, t=3) so you can trade memory for time on a small box.
//
// RFC 9106 describes a stronger "second recommended" profile for memory-constrained servers:
// m=64 MiB, t=3, p=4 (and a 2 GiB profile when you can afford it). Use it if your login volume
// and RAM allow; every request that hashes needs that much RAM for its duration, which is why
// PasswordHasher limits concurrent hashes. Re-measure on your own hardware: aim for roughly
// 50-250 ms per hash. When you raise these numbers, existing hashes keep working and are
// upgraded transparently at next login (Verify's needsRehash result).
var DefaultParams = Params{Memory: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}

// Bounds enforced when CONSTRUCTING a hasher and, more importantly, when PARSING a stored hash.
// A hash string comes from the database; if an attacker can write there (SQL injection, a
// poisoned backup restore) a stored "m=4194304,t=1000" would make one login attempt consume
// gigabytes and minutes. Refusing parameters outside a sane window turns that into an error.
const (
	minMemoryKiB   = 8 // the Argon2 specification minimum is 8*p
	maxMemoryKiB   = 256 * 1024
	maxIterations  = 10
	maxParallelism = 16
	minSaltLen     = 8
	maxSaltLen     = 64
	minKeyLen      = 16
	maxKeyLen      = 64
)

// MaxPasswordBytes bounds the password length we are willing to hash. Argon2 has no inherent
// limit (bcrypt silently stops at 72 bytes), but without any cap a client could send megabytes
// per login and make us hash them. 128 bytes is far beyond any human-typed password or
// password-manager output. It mirrors domain.MaxPasswordBytes: duplicated on purpose so this
// package enforces the rule itself.
const MaxPasswordBytes = 128

// Errors returned by Verify for stored hashes it cannot use. They describe a problem with the
// STORED value, never with the user's input.
var (
	// ErrInvalidHash: not a well-formed argon2id PHC string, or parameters outside the sane window.
	ErrInvalidHash = errors.New("auth: invalid password hash")
	// ErrUnsupportedAlgorithm: a well-formed PHC/crypt string for another algorithm. This is what
	// a legacy bcrypt hash ("$2a$12$...") looks like. See the README on migrating such users.
	ErrUnsupportedAlgorithm = errors.New("auth: unsupported password hash algorithm")
	// ErrIncompatibleVersion: argon2id, but not Argon2 version 0x13 (19), the only one we implement.
	ErrIncompatibleVersion = errors.New("auth: incompatible argon2 version")
	// ErrPasswordTooLong is returned by Hash for input over MaxPasswordBytes.
	ErrPasswordTooLong = errors.New("auth: password too long")
)

// validate reports whether p is inside the accepted window.
func (p Params) validate() error {
	switch {
	case p.Memory < minMemoryKiB || p.Memory > maxMemoryKiB:
		return fmt.Errorf("memory %d KiB outside [%d, %d]", p.Memory, minMemoryKiB, maxMemoryKiB)
	case p.Iterations < 1 || p.Iterations > maxIterations:
		return fmt.Errorf("iterations %d outside [1, %d]", p.Iterations, maxIterations)
	case p.Parallelism < 1 || p.Parallelism > maxParallelism:
		return fmt.Errorf("parallelism %d outside [1, %d]", p.Parallelism, maxParallelism)
	case p.Memory < 8*uint32(p.Parallelism):
		return fmt.Errorf("memory %d KiB below 8*parallelism", p.Memory)
	case p.SaltLen < minSaltLen || p.SaltLen > maxSaltLen:
		return fmt.Errorf("salt length %d outside [%d, %d]", p.SaltLen, minSaltLen, maxSaltLen)
	case p.KeyLen < minKeyLen || p.KeyLen > maxKeyLen:
		return fmt.Errorf("key length %d outside [%d, %d]", p.KeyLen, minKeyLen, maxKeyLen)
	}
	return nil
}

// PasswordHasher hashes and verifies passwords with Argon2id. Parameters are a field (not
// constants) so tests can use tiny, fast ones and production can raise them over time.
type PasswordHasher struct {
	params Params
	// sem bounds how many hashes run at once. Each one allocates Memory KiB (19 MiB by default):
	// without a bound, a burst of 200 concurrent logins would allocate ~4 GB and the OOM killer
	// would end the process. Excess callers wait for a slot instead.
	sem chan struct{}
}

// NewPasswordHasher returns a hasher using p. Use DefaultParams in production.
func NewPasswordHasher(p Params) (*PasswordHasher, error) {
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("auth: argon2 params: %w", err)
	}
	return &PasswordHasher{params: p, sem: make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))}, nil
}

// derive runs Argon2id, holding one concurrency slot while it uses its memory.
func (h *PasswordHasher) derive(password string, salt []byte, t, m uint32, p uint8, keyLen uint32) []byte {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()
	return argon2.IDKey([]byte(password), salt, t, m, p, keyLen)
}

// b64 is the encoding the PHC string format specifies: standard base64 alphabet, no padding.
// Strict() additionally rejects non-canonical encodings (trailing bits that are not zero), so
// there is exactly one valid string per value.
var b64 = base64.RawStdEncoding.Strict()

// Hash returns the PHC-format string
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// PHC ("Password Hashing Competition") string format is self-describing: algorithm, version,
// cost parameters and salt travel with the hash, so no separate salt column is needed and old
// hashes stay verifiable after you change the parameters. Every call draws a fresh random salt
// from crypto/rand, so hashing the same password twice gives different strings; salts defeat
// precomputed (rainbow) tables and make identical passwords look different.
func (h *PasswordHasher) Hash(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := h.derive(password, salt, h.params.Iterations, h.params.Memory, h.params.Parallelism, h.params.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.Memory, h.params.Iterations, h.params.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify checks password against a stored PHC string.
//
//   - ok reports whether the password matches. A wrong password is (false, false, nil): it is the
//     normal "no", not an error.
//   - needsRehash is true when the password matched but the stored hash was made with different
//     parameters than this hasher's current ones. The caller should then Hash the password again
//     and store the new string: that is how you raise the cost of every account over time without
//     a forced password reset ("rehash on login": the plaintext is only available at login).
//   - err is non-nil only when the STORED hash is unusable (ErrInvalidHash,
//     ErrUnsupportedAlgorithm, ErrIncompatibleVersion). Parsing is strict.
//
// The recomputed key is compared with crypto/subtle.ConstantTimeCompare so the comparison time
// does not reveal how many leading bytes matched.
func (h *PasswordHasher) Verify(password, encoded string) (ok, needsRehash bool, err error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	if len(password) > MaxPasswordBytes {
		return false, false, nil
	}
	got := h.derive(password, salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, p != h.params, nil
}

// decodeHash parses and validates a PHC string. It returns the parameters it carried (SaltLen and
// KeyLen are derived from the decoded lengths so they compare directly with the current Params).
func decodeHash(encoded string) (p Params, salt, key []byte, err error) {
	// "$argon2id$v=19$m=19456,t=2,p=1$salt$hash" splits into 6 parts, the first empty.
	parts := strings.Split(encoded, "$")
	if len(parts) < 2 || parts[0] != "" {
		return Params{}, nil, nil, fmt.Errorf("%w: not a PHC string", ErrInvalidHash)
	}
	if parts[1] != "argon2id" {
		// Includes argon2i/argon2d (deliberately refused) and bcrypt's "$2a$".
		return Params{}, nil, nil, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, parts[1])
	}
	if len(parts) != 6 {
		return Params{}, nil, nil, fmt.Errorf("%w: expected 5 fields, got %d", ErrInvalidHash, len(parts)-1)
	}

	version, ok := strings.CutPrefix(parts[2], "v=")
	if !ok {
		return Params{}, nil, nil, fmt.Errorf("%w: missing version", ErrInvalidHash)
	}
	v, err := strconv.ParseUint(version, 10, 32)
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: bad version", ErrInvalidHash)
	}
	if v != argon2.Version {
		return Params{}, nil, nil, fmt.Errorf("%w: %d", ErrIncompatibleVersion, v)
	}

	// Exactly "m=<uint>,t=<uint>,p=<uint>", in that order.
	kv := strings.Split(parts[3], ",")
	if len(kv) != 3 {
		return Params{}, nil, nil, fmt.Errorf("%w: bad parameters", ErrInvalidHash)
	}
	var vals [3]uint64
	for i, name := range [3]string{"m=", "t=", "p="} {
		raw, ok := strings.CutPrefix(kv[i], name)
		if !ok {
			return Params{}, nil, nil, fmt.Errorf("%w: bad parameters", ErrInvalidHash)
		}
		bits := 32
		if name == "p=" {
			bits = 8
		}
		if vals[i], err = strconv.ParseUint(raw, 10, bits); err != nil {
			return Params{}, nil, nil, fmt.Errorf("%w: bad parameters", ErrInvalidHash)
		}
	}

	if salt, err = b64.DecodeString(parts[4]); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: bad salt", ErrInvalidHash)
	}
	if key, err = b64.DecodeString(parts[5]); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: bad hash", ErrInvalidHash)
	}

	p = Params{
		Memory: uint32(vals[0]), Iterations: uint32(vals[1]), Parallelism: uint8(vals[2]),
		SaltLen: uint32(len(salt)), KeyLen: uint32(len(key)),
	}
	// Bounds are checked BEFORE any hashing happens: this is the DoS guard described on the consts.
	if err := p.validate(); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: %v", ErrInvalidHash, err)
	}
	return p, salt, key, nil
}
