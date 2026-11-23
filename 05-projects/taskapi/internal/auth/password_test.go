package auth

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fastParams keeps tests quick (a few milliseconds per hash). Never use them in production.
var fastParams = Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func newHasher(t *testing.T, p Params) *PasswordHasher {
	t.Helper()
	h, err := NewPasswordHasher(p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPasswordRoundTrip(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	hash, err := h.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("hash contains the plaintext")
	}
	// PHC layout: $argon2id$v=19$m=..,t=..,p=..$<b64 salt>$<b64 hash>, unpadded standard base64.
	re := regexp.MustCompile(`^\$argon2id\$v=19\$m=64,t=1,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !re.MatchString(hash) {
		t.Fatalf("unexpected PHC format: %s", hash)
	}

	ok, rehash, err := h.Verify("correct horse battery", hash)
	if err != nil || !ok || rehash {
		t.Fatalf("Verify(right) = %v, %v, %v; want true, false, nil", ok, rehash, err)
	}
	// Same password, different random salt -> different string, both valid.
	hash2, _ := h.Hash("correct horse battery")
	if hash == hash2 {
		t.Fatal("two hashes are identical: salt is not random")
	}
	if ok, _, _ := h.Verify("correct horse battery", hash2); !ok {
		t.Fatal("second hash does not verify")
	}
}

func TestPasswordWrong(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	hash, _ := h.Hash("correct horse battery")
	for _, pw := range []string{"", "wrong", "correct horse batter", "Correct horse battery", strings.Repeat("x", MaxPasswordBytes+1)} {
		ok, rehash, err := h.Verify(pw, hash)
		if ok || rehash || err != nil {
			t.Errorf("Verify(%.10q) = %v, %v, %v; want false, false, nil (a wrong password is not an error)", pw, ok, rehash, err)
		}
	}
}

func TestPasswordTooLong(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	if _, err := h.Hash(strings.Repeat("x", MaxPasswordBytes)); err != nil {
		t.Fatalf("exactly the limit must be accepted: %v", err)
	}
	if _, err := h.Hash(strings.Repeat("x", MaxPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("got %v, want ErrPasswordTooLong", err)
	}
	// Unlike bcrypt, bytes past 72 matter.
	long := strings.Repeat("a", 100)
	hash, _ := h.Hash(long)
	if ok, _, _ := h.Verify(long[:99]+"b", hash); ok {
		t.Fatal("argon2id must not ignore bytes beyond 72")
	}
}

func TestPasswordTamperedHash(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	hash, _ := h.Hash("pw-pw-pw-pw")
	parts := strings.Split(hash, "$") // "", argon2id, v=19, params, salt, key

	flip := func(s string) string { // change the first character to a different base64 character
		if s[0] == 'A' {
			return "B" + s[1:]
		}
		return "A" + s[1:]
	}
	tests := map[string]string{
		"changed key":    strings.Join([]string{"", parts[1], parts[2], parts[3], parts[4], flip(parts[5])}, "$"),
		"changed salt":   strings.Join([]string{"", parts[1], parts[2], parts[3], flip(parts[4]), parts[5]}, "$"),
		"changed memory": strings.Join([]string{"", parts[1], parts[2], "m=128,t=1,p=1", parts[4], parts[5]}, "$"),
		"changed time":   strings.Join([]string{"", parts[1], parts[2], "m=64,t=2,p=1", parts[4], parts[5]}, "$"),
	}
	for name, tampered := range tests {
		ok, _, err := h.Verify("pw-pw-pw-pw", tampered)
		if ok {
			t.Errorf("%s: tampered hash verified", name)
		}
		if err != nil {
			t.Errorf("%s: err = %v; a well-formed but different hash is a plain mismatch", name, err)
		}
	}
}

func TestVerifyRejectsOtherAlgorithms(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	hash, _ := h.Hash("pw-pw-pw-pw")
	argon2i := strings.Replace(hash, "$argon2id$", "$argon2i$", 1)
	argon2d := strings.Replace(hash, "$argon2id$", "$argon2d$", 1)
	// A real bcrypt hash of "password" (cost 4), the legacy format from the earlier version.
	bcryptHash := "$2a$04$Ij0ZtYtHPkJGJqjhYQnmM.8a1Tq9yKj3hv8ZQeE4Ps1kUp6uW0fWq"

	for name, in := range map[string]string{"argon2i": argon2i, "argon2d": argon2d, "bcrypt": bcryptHash, "scrypt": "$scrypt$ln=15,r=8,p=1$c2FsdA$aGFzaA"} {
		ok, _, err := h.Verify("pw-pw-pw-pw", in)
		if ok || !errors.Is(err, ErrUnsupportedAlgorithm) {
			t.Errorf("%s: Verify = %v, %v; want false, ErrUnsupportedAlgorithm", name, ok, err)
		}
	}
	// A different Argon2 version is refused, not silently verified with the wrong algorithm.
	old := strings.Replace(hash, "v=19", "v=16", 1)
	if _, _, err := h.Verify("pw-pw-pw-pw", old); !errors.Is(err, ErrIncompatibleVersion) {
		t.Errorf("v=16: got %v, want ErrIncompatibleVersion", err)
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()
	weak := newHasher(t, fastParams)
	hash, _ := weak.Hash("pw-pw-pw-pw")

	stronger := fastParams
	stronger.Memory, stronger.Iterations = 128, 2
	for name, p := range map[string]Params{
		"memory+time": stronger,
		"parallelism": {Memory: 64, Iterations: 1, Parallelism: 2, SaltLen: 16, KeyLen: 32},
		"key length":  {Memory: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 48},
		"salt length": {Memory: 64, Iterations: 1, Parallelism: 1, SaltLen: 24, KeyLen: 32},
	} {
		h := newHasher(t, p)
		ok, rehash, err := h.Verify("pw-pw-pw-pw", hash)
		if !ok || !rehash || err != nil {
			t.Errorf("%s: Verify = %v, %v, %v; want true, true, nil", name, ok, rehash, err)
		}
		// The upgraded hash is current: no further rehash.
		upgraded, _ := h.Hash("pw-pw-pw-pw")
		if ok, rehash, _ := h.Verify("pw-pw-pw-pw", upgraded); !ok || rehash {
			t.Errorf("%s: upgraded hash reports rehash=%v ok=%v", name, rehash, ok)
		}
	}
	// A wrong password never asks for a rehash (the caller does not have the plaintext).
	if ok, rehash, _ := newHasher(t, stronger).Verify("nope-nope", hash); ok || rehash {
		t.Error("wrong password must not report needsRehash")
	}
}

func TestVerifyMalformed(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	salt, key := "c2FsdHNhbHRzYWx0c2FsdA", "aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g" // valid unpadded base64 of 16 / 32 bytes
	tests := map[string]string{
		"empty":              "",
		"plain text":         "hunter2",
		"no leading dollar":  "argon2id$v=19$m=64,t=1,p=1$" + salt + "$" + key,
		"too few fields":     "$argon2id$v=19$m=64,t=1,p=1$" + salt,
		"too many fields":    "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$" + key + "$extra",
		"missing version":    "$argon2id$m=64,t=1,p=1$" + salt + "$" + key + "$x",
		"bad version":        "$argon2id$v=abc$m=64,t=1,p=1$" + salt + "$" + key,
		"params wrong order": "$argon2id$v=19$t=1,m=64,p=1$" + salt + "$" + key,
		"params missing":     "$argon2id$v=19$m=64,t=1$" + salt + "$" + key,
		"params not number":  "$argon2id$v=19$m=lots,t=1,p=1$" + salt + "$" + key,
		"params negative":    "$argon2id$v=19$m=-64,t=1,p=1$" + salt + "$" + key,
		"parallelism > 255":  "$argon2id$v=19$m=64,t=1,p=300$" + salt + "$" + key,
		"salt not base64":    "$argon2id$v=19$m=64,t=1,p=1$!!!!$" + key,
		"hash not base64":    "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$!!!!",
		"padded base64":      "$argon2id$v=19$m=64,t=1,p=1$" + salt + "==$" + key,
		"empty salt":         "$argon2id$v=19$m=64,t=1,p=1$$" + key,
		"empty hash":         "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$",
	}
	for name, in := range tests {
		ok, rehash, err := h.Verify("anything", in)
		if ok || rehash || err == nil {
			t.Errorf("%s: Verify = %v, %v, %v; want an error", name, ok, rehash, err)
		}
	}
}

// The DoS guard: absurd parameters in a stored hash must be rejected BEFORE any memory is
// allocated. If this test ever hangs or OOMs, the bounds check moved after the hashing.
func TestVerifyParamBounds(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	salt, key := "c2FsdHNhbHRzYWx0c2FsdA", "aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	for name, params := range map[string]string{
		"memory 4 GiB":     "m=4194304,t=1,p=1",
		"memory above max": "m=262145,t=1,p=1",
		"memory 0":         "m=0,t=1,p=1",
		"memory below 8*p": "m=8,t=1,p=2",
		"iterations 1000":  "m=64,t=1000,p=1",
		"iterations 0":     "m=64,t=0,p=1",
		"parallelism 0":    "m=64,t=1,p=0",
		"parallelism 17":   "m=1024,t=1,p=17",
	} {
		_, _, err := h.Verify("x", "$argon2id$v=19$"+params+"$"+salt+"$"+key)
		if !errors.Is(err, ErrInvalidHash) {
			t.Errorf("%s: got %v, want ErrInvalidHash", name, err)
		}
	}
	// Salt and key length bounds.
	shortSalt := "$argon2id$v=19$m=64,t=1,p=1$YWJj$" + key // 3 bytes
	if _, _, err := h.Verify("x", shortSalt); !errors.Is(err, ErrInvalidHash) {
		t.Errorf("short salt: got %v", err)
	}
	shortKey := "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$YWJj"
	if _, _, err := h.Verify("x", shortKey); !errors.Is(err, ErrInvalidHash) {
		t.Errorf("short key: got %v", err)
	}
}

func TestNewPasswordHasherRejectsBadParams(t *testing.T) {
	t.Parallel()
	good := fastParams
	mutate := map[string]func(*Params){
		"memory too small": func(p *Params) { p.Memory = 4 },
		"memory too big":   func(p *Params) { p.Memory = 1 << 22 },
		"zero iterations":  func(p *Params) { p.Iterations = 0 },
		"zero parallelism": func(p *Params) { p.Parallelism = 0 },
		"short salt":       func(p *Params) { p.SaltLen = 4 },
		"short key":        func(p *Params) { p.KeyLen = 8 },
	}
	for name, f := range mutate {
		p := good
		f(&p)
		if _, err := NewPasswordHasher(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewPasswordHasher(DefaultParams); err != nil {
		t.Fatalf("DefaultParams rejected: %v", err)
	}
}

// Many hashes at once must all succeed and stay correct (exercises the concurrency limiter).
func TestHasherConcurrent(t *testing.T) {
	t.Parallel()
	h := newHasher(t, fastParams)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			pw := strings.Repeat("p", 8+i)
			hash, err := h.Hash(pw)
			if err != nil {
				t.Error(err)
				return
			}
			if ok, _, err := h.Verify(pw, hash); !ok || err != nil {
				t.Errorf("concurrent verify failed: %v %v", ok, err)
			}
		})
	}
	wg.Wait()
}

// Documents the production defaults so a change to them is a conscious, reviewed one.
func TestDefaultParamsMatchOWASP(t *testing.T) {
	t.Parallel()
	want := Params{Memory: 19456, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}
	if DefaultParams != want {
		t.Fatalf("DefaultParams = %+v, want %+v (OWASP: m=19 MiB, t=2, p=1)", DefaultParams, want)
	}
}
