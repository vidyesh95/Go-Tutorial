package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func load(t *testing.T, env map[string]string, args ...string) (Config, error) {
	t.Helper()
	return loadConfig(func(k string) string { return env[k] }, args, io.Discard)
}

func TestDefaults(t *testing.T) {
	cfg, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Env != "dev" || cfg.Addr != ":8080" || cfg.ReadTimeout != 5*time.Second || cfg.MaxConns != 100 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestPrecedenceDefaultsEnvFlags(t *testing.T) {
	env := map[string]string{"APP_ADDR": ":9000", "APP_MAX_CONNS": "7", "APP_READ_TIMEOUT": "250ms"}

	cfg, err := load(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9000" || cfg.MaxConns != 7 || cfg.ReadTimeout != 250*time.Millisecond {
		t.Errorf("env not applied: %+v", cfg)
	}

	cfg, err = load(t, env, "-addr", ":7000", "-log-level", "debug")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":7000" {
		t.Errorf("flag did not override env: %q", cfg.Addr)
	}
	if cfg.MaxConns != 7 {
		t.Errorf("unset flag clobbered env value: %d", cfg.MaxConns)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("level = %v", cfg.LogLevel)
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	_, err := load(t, map[string]string{
		"APP_ENV": "prod", "APP_READ_TIMEOUT": "never", "APP_MAX_CONNS": "x", "APP_LOG_LEVEL": "loud",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"APP_READ_TIMEOUT", "APP_MAX_CONNS", "APP_LOG_LEVEL", "APP_DB_URL", "APP_API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"bad env name", map[string]string{"APP_ENV": "qa"}, nil},
		{"bad addr", map[string]string{"APP_ADDR": "8080"}, nil},
		{"zero timeout", map[string]string{"APP_READ_TIMEOUT": "0s"}, nil},
		{"too many conns", nil, []string{"-max-conns", "5000"}},
		{"unknown flag", nil, []string{"-nope"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := load(t, tt.env, tt.args...); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestProdRequiresSecretsAndAcceptsThem(t *testing.T) {
	if _, err := load(t, map[string]string{"APP_ENV": "prod"}); err == nil {
		t.Error("prod without secrets must fail")
	}
	cfg, err := load(t, map[string]string{
		"APP_ENV": "prod", "APP_DB_URL": "postgres://u:pw@h/db", "APP_API_KEY": "0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBURL.Reveal() != "postgres://u:pw@h/db" {
		t.Error("Reveal must return the real value")
	}
}

const secretValue = "hunter2-super-secret"

func TestSecretsNeverLeak(t *testing.T) {
	cfg := defaultConfig()
	cfg.DBURL = Secret(secretValue)
	cfg.APIKey = Secret(secretValue)

	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var textLog, jsonLog bytes.Buffer
	slog.New(slog.NewTextHandler(&textLog, nil)).Info("c", "config", cfg, "key", cfg.APIKey)
	slog.New(slog.NewJSONHandler(&jsonLog, nil)).Info("c", "config", cfg, "key", cfg.APIKey)

	outputs := map[string]string{
		"%v":        fmt.Sprintf("%v", cfg),
		"%+v":       fmt.Sprintf("%+v", cfg),
		"%#v":       fmt.Sprintf("%#v", cfg),
		"%s":        fmt.Sprintf("%s", cfg.APIKey),
		"json":      string(jsonBytes),
		"slog txt":  textLog.String(),
		"slog json": jsonLog.String(),
	}
	for name, out := range outputs {
		if strings.Contains(out, secretValue) {
			t.Errorf("%s leaked the secret: %s", name, out)
		}
	}
	if !strings.Contains(outputs["slog json"], "[REDACTED]") {
		t.Error("expected [REDACTED] marker in logs")
	}
}

func TestLoggerHandlerByEnv(t *testing.T) {
	var buf bytes.Buffer
	cfg := defaultConfig()
	newLogger(cfg, &buf).Info("hello")
	if json.Valid(buf.Bytes()) {
		t.Error("dev should log human-readable text, got JSON")
	}

	buf.Reset()
	cfg.Env = "prod"
	newLogger(cfg, &buf).Info("hello")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("prod should log JSON: %v\n%s", err, buf.String())
	}
	if line["env"] != "prod" {
		t.Errorf("env attribute missing: %v", line)
	}
}

func TestLogLevelIsRespected(t *testing.T) {
	var buf bytes.Buffer
	cfg := defaultConfig()
	cfg.LogLevel = slog.LevelWarn
	l := newLogger(cfg, &buf)
	l.Info("hidden")
	l.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Errorf("level filtering wrong: %s", buf.String())
	}
}

// With no globals, independent Applications can run at the same time (and in parallel tests).
func TestApplicationsAreIndependent(t *testing.T) {
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			cfg := defaultConfig()
			app := newApplication(cfg, newLogger(cfg, io.Discard), &Store{})
			h := app.routes()
			var last string
			for range i + 1 {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("GET", "/hits", nil))
				last = strings.TrimSpace(rec.Body.String())
			}
			if want := fmt.Sprintf("hits=%d", i+1); last != want {
				t.Errorf("app %d: %s, want %s", i, last, want)
			}
		})
	}
	wg.Wait()
}

func TestConfigEndpointRedacts(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = Secret(secretValue)
	app := newApplication(cfg, newLogger(cfg, io.Discard), &Store{})
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, httptest.NewRequest("GET", "/config", nil))
	if strings.Contains(rec.Body.String(), secretValue) {
		t.Errorf("/config leaked: %s", rec.Body)
	}
}
