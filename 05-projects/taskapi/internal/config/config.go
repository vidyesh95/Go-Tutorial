// Package config turns environment variables into a validated Config.
//
// Why environment variables: the twelve-factor way. The same binary/container image runs in dev,
// CI and production, and only the environment differs. Secrets in particular must come from the
// environment (or a secrets manager), never from the repository or the image.
//
// Load takes a getenv function instead of calling os.Getenv itself. Production passes os.Getenv;
// tests pass a map lookup, so no test ever touches (or races on) real process environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// MinSecretBytes is the minimum JWT signing-key length. HS256 uses HMAC-SHA256, whose security
// is bounded by the key size; RFC 7518 requires a key at least as long as the hash output
// (256 bits = 32 bytes). Short keys make brute-forcing the signature realistic.
const MinSecretBytes = 32

// Config is the full runtime configuration of the service.
type Config struct {
	Addr            string
	DBPath          string
	JWTSecret       Secret
	JWTTTL          time.Duration
	LogLevel        slog.Level
	LogFormat       string // "json" or "text"
	RateRPS         float64
	RateBurst       int
	CORSOrigins     []string
	ShutdownTimeout time.Duration
}

// Secret is a string that refuses to print itself. Every formatting path (%v, %+v, %#v, %s and
// slog) returns a placeholder, so logging the whole Config by accident cannot leak the key.
// Code that truly needs the value must call Reveal, which is easy to grep for in review.
type Secret string

func (Secret) redacted() string { return "[REDACTED]" }

// Reveal returns the underlying secret value.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (s Secret) String() string { return s.redacted() }

// GoString implements fmt.GoStringer (used by %#v).
func (s Secret) GoString() string { return s.redacted() }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.redacted()) }

// LogValue lets the whole Config be logged at startup as one structured group.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.Addr),
		slog.String("db_path", c.DBPath),
		slog.Any("jwt_secret", c.JWTSecret),
		slog.String("jwt_ttl", c.JWTTTL.String()),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", c.LogFormat),
		slog.Float64("rate_rps", c.RateRPS),
		slog.Int("rate_burst", c.RateBurst),
		slog.String("cors_origins", strings.Join(c.CORSOrigins, ",")),
		slog.String("shutdown_timeout", c.ShutdownTimeout.String()),
	)
}

// Load reads and validates configuration. It reports ALL problems at once (errors.Join) so an
// operator fixes the whole environment in one go rather than one restart per mistake.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	fail := func(name, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", name, fmt.Sprintf(format, args...)))
	}
	// str returns the variable or def when unset/blank.
	str := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Addr:      str("TASKAPI_ADDR", ":8080"),
		DBPath:    str("TASKAPI_DB_PATH", "taskapi.db"),
		LogFormat: strings.ToLower(str("TASKAPI_LOG_FORMAT", "json")),
	}

	// Required, no default: a default secret in source code is a published secret.
	secret := getenv("TASKAPI_JWT_SECRET")
	switch {
	case secret == "":
		fail("TASKAPI_JWT_SECRET", "required (generate one with: openssl rand -base64 48)")
	case len(secret) < MinSecretBytes:
		// Report the length, never the value.
		fail("TASKAPI_JWT_SECRET", "must be at least %d bytes, got %d", MinSecretBytes, len(secret))
	default:
		cfg.JWTSecret = Secret(secret)
	}

	var err error
	if cfg.JWTTTL, err = time.ParseDuration(str("TASKAPI_JWT_TTL", "24h")); err != nil || cfg.JWTTTL <= 0 {
		fail("TASKAPI_JWT_TTL", "must be a positive duration like 24h or 30m")
	}
	if cfg.ShutdownTimeout, err = time.ParseDuration(str("TASKAPI_SHUTDOWN_TIMEOUT", "15s")); err != nil || cfg.ShutdownTimeout <= 0 {
		fail("TASKAPI_SHUTDOWN_TIMEOUT", "must be a positive duration like 15s")
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(str("TASKAPI_LOG_LEVEL", "info"))); err != nil {
		fail("TASKAPI_LOG_LEVEL", "must be one of debug, info, warn, error")
	}
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		fail("TASKAPI_LOG_FORMAT", "must be json or text")
	}
	if cfg.RateRPS, err = strconv.ParseFloat(str("TASKAPI_RATE_RPS", "10"), 64); err != nil || cfg.RateRPS <= 0 {
		fail("TASKAPI_RATE_RPS", "must be a positive number")
	}
	if cfg.RateBurst, err = strconv.Atoi(str("TASKAPI_RATE_BURST", "20")); err != nil || cfg.RateBurst < 1 {
		fail("TASKAPI_RATE_BURST", "must be a positive integer")
	}
	for origin := range strings.SplitSeq(getenv("TASKAPI_CORS_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			cfg.CORSOrigins = append(cfg.CORSOrigins, origin)
		}
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}
