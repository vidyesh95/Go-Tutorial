package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const goodSecret = "0123456789abcdef0123456789abcdef" // exactly 32 bytes

// env builds a getenv function from a map; unset names return "".
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"TASKAPI_JWT_SECRET": goodSecret}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.DBPath != "taskapi.db" || cfg.JWTTTL != 24*time.Hour ||
		cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "json" || cfg.RateRPS != 10 ||
		cfg.RateBurst != 20 || cfg.ShutdownTimeout != 15*time.Second || len(cfg.CORSOrigins) != 0 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.JWTSecret.Reveal() != goodSecret {
		t.Fatal("secret not loaded")
	}
	// Cookie-session defaults: 7 days, slide after 24h, Secure ON (safe by default), GitHub off.
	if cfg.SessionTTL != 168*time.Hour || cfg.SessionUpdateAge != 24*time.Hour || !cfg.CookieSecure ||
		cfg.PublicURL != "http://localhost:8080" || cfg.AuthRateRPS != 0.5 || cfg.AuthRateBurst != 10 || cfg.GitHubEnabled() {
		t.Fatalf("unexpected session defaults: %+v", cfg)
	}
}

func TestLoadSessionAuthOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"TASKAPI_JWT_SECRET":           goodSecret,
		"TASKAPI_SESSION_TTL":          "48h",
		"TASKAPI_SESSION_UPDATE_AGE":   "1h",
		"TASKAPI_COOKIE_SECURE":        "false",
		"TASKAPI_PUBLIC_URL":           "https://api.example.com/",
		"TASKAPI_AUTH_RATE_RPS":        "2",
		"TASKAPI_AUTH_RATE_BURST":      "3",
		"TASKAPI_GITHUB_CLIENT_ID":     "id",
		"TASKAPI_GITHUB_CLIENT_SECRET": "shh-github-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionTTL != 48*time.Hour || cfg.SessionUpdateAge != time.Hour || cfg.CookieSecure ||
		cfg.PublicURL != "https://api.example.com" || cfg.AuthRateRPS != 2 || cfg.AuthRateBurst != 3 || !cfg.GitHubEnabled() {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if strings.Contains(fmt.Sprintf("%+v", cfg), "shh-github-secret") {
		t.Fatal("github client secret leaks when the config is printed")
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"TASKAPI_JWT_SECRET":       goodSecret,
		"TASKAPI_ADDR":             "127.0.0.1:9000",
		"TASKAPI_JWT_TTL":          "30m",
		"TASKAPI_LOG_LEVEL":        "debug",
		"TASKAPI_LOG_FORMAT":       "TEXT",
		"TASKAPI_RATE_RPS":         "2.5",
		"TASKAPI_RATE_BURST":       "5",
		"TASKAPI_CORS_ORIGINS":     " https://a.example , https://b.example,,",
		"TASKAPI_SHUTDOWN_TIMEOUT": "3s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9000" || cfg.JWTTTL != 30*time.Minute || cfg.LogLevel != slog.LevelDebug ||
		cfg.LogFormat != "text" || cfg.RateRPS != 2.5 || cfg.RateBurst != 5 || cfg.ShutdownTimeout != 3*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if got := strings.Join(cfg.CORSOrigins, "|"); got != "https://a.example|https://b.example" {
		t.Fatalf("cors origins = %q", got)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantSub []string
	}{
		{"missing secret", map[string]string{}, []string{"TASKAPI_JWT_SECRET: required"}},
		{"short secret", map[string]string{"TASKAPI_JWT_SECRET": "too-short"}, []string{"at least 32 bytes, got 9"}},
		{"bad values are all reported", map[string]string{
			"TASKAPI_JWT_SECRET": goodSecret, "TASKAPI_JWT_TTL": "soon", "TASKAPI_LOG_LEVEL": "loud",
			"TASKAPI_LOG_FORMAT": "xml", "TASKAPI_RATE_RPS": "0", "TASKAPI_RATE_BURST": "x",
			"TASKAPI_SHUTDOWN_TIMEOUT": "-1s",
		}, []string{"TASKAPI_JWT_TTL", "TASKAPI_LOG_LEVEL", "TASKAPI_LOG_FORMAT", "TASKAPI_RATE_RPS", "TASKAPI_RATE_BURST", "TASKAPI_SHUTDOWN_TIMEOUT"}},
		{"session durations", map[string]string{
			"TASKAPI_JWT_SECRET": goodSecret, "TASKAPI_SESSION_TTL": "7d", "TASKAPI_SESSION_UPDATE_AGE": "0s",
		}, []string{"TASKAPI_SESSION_TTL", "TASKAPI_SESSION_UPDATE_AGE"}},
		{"update age above ttl", map[string]string{
			"TASKAPI_JWT_SECRET": goodSecret, "TASKAPI_SESSION_TTL": "1h", "TASKAPI_SESSION_UPDATE_AGE": "2h",
		}, []string{"must not exceed TASKAPI_SESSION_TTL"}},
		{"bad cookie flag and url", map[string]string{
			"TASKAPI_JWT_SECRET": goodSecret, "TASKAPI_COOKIE_SECURE": "maybe", "TASKAPI_PUBLIC_URL": "not a url",
		}, []string{"TASKAPI_COOKIE_SECURE", "TASKAPI_PUBLIC_URL"}},
		{"github id without secret", map[string]string{
			"TASKAPI_JWT_SECRET": goodSecret, "TASKAPI_GITHUB_CLIENT_ID": "id",
		}, []string{"must be set together"}},
		{"github secret without id", map[string]string{
			"TASKAPI_JWT_SECRET": goodSecret, "TASKAPI_GITHUB_CLIENT_SECRET": "s",
		}, []string{"must be set together"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.env))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not mention %q", err, sub)
				}
			}
			if strings.Contains(err.Error(), "too-short") {
				t.Error("error leaks the secret value")
			}
		})
	}
}

func TestSecretNeverPrints(t *testing.T) {
	cfg, err := Load(env(map[string]string{"TASKAPI_JWT_SECRET": goodSecret}))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	logger := slog.New(slog.NewJSONHandler(&sb, nil))
	logger.Info("config", "cfg", cfg, "secret", cfg.JWTSecret)

	for name, out := range map[string]string{
		"slog":   sb.String(),
		"%v":     fmt.Sprintf("%v", cfg),
		"%+v":    fmt.Sprintf("%+v", cfg),
		"%#v":    fmt.Sprintf("%#v", cfg),
		"%s":     fmt.Sprintf("%s", cfg.JWTSecret),
		"secret": fmt.Sprint(cfg.JWTSecret),
	} {
		if strings.Contains(out, goodSecret) {
			t.Errorf("%s output leaks the secret: %s", name, out)
		}
	}
	if !strings.Contains(sb.String(), "[REDACTED]") {
		t.Errorf("expected redaction marker in %s", sb.String())
	}
}
