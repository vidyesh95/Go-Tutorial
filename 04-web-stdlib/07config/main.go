// Lesson 07: configuration, secrets and dependency passing
//
// The Twelve-Factor App says: store config in the ENVIRONMENT. The same build artifact
// (one container image) runs in dev, staging and prod, and only the environment differs.
//
// Layering used here, lowest to highest priority:
//
//	built-in defaults  <  environment variables  <  command-line flags
//
// Defaults make local development zero-setup. Env vars are what Kubernetes/Docker/systemd
// give you. Flags let a human override one value for one run (`-addr :9090`).
//
// Rules this lesson demonstrates:
//   - Load config ONCE at startup into a typed struct, then pass it around. Do not call
//     os.Getenv from deep inside business code: it is invisible coupling and untestable.
//   - loadConfig takes getenv and args as PARAMETERS (not os.Getenv / os.Args), so tests and
//     this demo feed it fake environments without touching the process environment.
//   - Parse into real types (time.Duration, int, slog.Level) right away. "5s" -> 5*time.Second.
//   - Validate everything and report ALL problems together (errors.Join), so an operator
//     fixes one deploy instead of ten. Fail fast at startup, never at 3am on first use.
//   - Secrets (DB passwords, API keys) get their own type whose String/LogValue/MarshalText
//     all return "[REDACTED]". Printing, logging or JSON-encoding the config then cannot leak
//     them, even if somebody later does `slog.Info("config", "cfg", cfg)`. The only way to
//     get the value is the explicit Reveal() call, which is easy to grep for in code review.
//   - NO GLOBAL STATE. No package-level logger, config or *sql.DB. Everything a handler needs
//     lives in an Application struct that main builds and hands out (dependency injection by
//     hand). Tests then build their own Application with a fake store and a discard logger,
//     and tests can run in parallel.
//   - log/slog: human-friendly TextHandler for dev, JSON handler for prod (log pipelines such as
//     Loki/Datadog/CloudWatch ingest JSON). One logger constructor picks by config.
//
// Environment variables (prefix APP_ avoids clashing with other software):
//
//	APP_ENV           dev | staging | prod            (default dev)
//	APP_ADDR          listen address                  (default :8080)
//	APP_LOG_LEVEL     debug | info | warn | error     (default info)
//	APP_READ_TIMEOUT  Go duration, e.g. 500ms, 5s     (default 5s)
//	APP_MAX_CONNS     integer 1..1000                 (default 100)
//	APP_DB_URL        secret, required in prod
//	APP_API_KEY       secret (>= 16 chars), required in prod
//
// Default mode runs scripted scenarios with fake environments. With -serve it uses the real
// environment and arguments:
//
//	APP_ENV=prod APP_DB_URL=postgres://u:pw@db/app APP_API_KEY=0123456789abcdef \
//	    go run ./04-web-stdlib/07config -serve -addr :8080 -log-level debug
//	curl localhost:8080/config     # secrets show as [REDACTED]
//	curl localhost:8080/hits
//
// In industry: libraries such as kelseyhightower/envconfig, caarlos0/env or spf13/viper reduce
// the parsing boilerplate, but the principles are identical. Secrets usually arrive as env vars or
// mounted files from Vault / AWS Secrets Manager / Kubernetes Secrets, never from the repo. For
// non-secret structured config, teams also use YAML/TOML files, kept strictly in sync with env.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"sync"
	"time"
)

// ---------- Secret ----------

// Secret holds a sensitive string. Every way of rendering it prints "[REDACTED]".
type Secret string

const redacted = "[REDACTED]"

func (Secret) String() string               { return redacted }                   // fmt %v / %s
func (Secret) GoString() string             { return redacted }                   // fmt %#v
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) } // slog.LogValuer
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }      // encoding/json, YAML...

// Reveal returns the real value. Use it only at the point of use (open the DB, sign a request).
func (s Secret) Reveal() string { return string(s) }

// ---------- Config ----------

type Config struct {
	Env         string
	Addr        string
	LogLevel    slog.Level
	ReadTimeout time.Duration
	MaxConns    int
	DBURL       Secret
	APIKey      Secret

	Serve bool // CLI-only (-serve): run the real server instead of the demo
}

func defaultConfig() Config {
	return Config{
		Env:         "dev",
		Addr:        ":8080",
		LogLevel:    slog.LevelInfo,
		ReadTimeout: 5 * time.Second,
		MaxConns:    100,
	}
}

// LogValue makes `logger.Info("config", "config", cfg)` print a clean group. Secret fields
// are themselves LogValuers, so slog resolves them to "[REDACTED]" automatically.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.String("addr", c.Addr),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("read_timeout", c.ReadTimeout.String()),
		slog.Int("max_conns", c.MaxConns),
		slog.Any("db_url", c.DBURL),
		slog.Any("api_key", c.APIKey),
	)
}

// envReader reads typed values and remembers parse errors instead of stopping at the first.
type envReader struct {
	get  func(string) string
	errs []error
}

func (e *envReader) str(key, def string) string {
	if v := e.get(key); v != "" {
		return v
	}
	return def
}

func (e *envReader) duration(key string, def time.Duration) time.Duration {
	v := e.get(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not a duration (try 500ms, 5s, 2m)", key, v))
		return def
	}
	return d
}

func (e *envReader) integer(key string, def int) int {
	v := e.get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

func (e *envReader) level(key string, def slog.Level) slog.Level {
	v := e.get(key)
	if v == "" {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not a log level (debug, info, warn, error)", key, v))
		return def
	}
	return l
}

// loadConfig = defaults, then env, then flags, then validation.
// getenv and args are injected so it is trivially testable.
func loadConfig(getenv func(string) string, args []string, usage io.Writer) (Config, error) {
	cfg := defaultConfig()

	// 1+2: defaults overridden by environment.
	env := &envReader{get: getenv}
	cfg.Env = env.str("APP_ENV", cfg.Env)
	cfg.Addr = env.str("APP_ADDR", cfg.Addr)
	cfg.LogLevel = env.level("APP_LOG_LEVEL", cfg.LogLevel)
	cfg.ReadTimeout = env.duration("APP_READ_TIMEOUT", cfg.ReadTimeout)
	cfg.MaxConns = env.integer("APP_MAX_CONNS", cfg.MaxConns)
	cfg.DBURL = Secret(env.str("APP_DB_URL", ""))
	cfg.APIKey = Secret(env.str("APP_API_KEY", ""))

	// 3: flags win. Each flag's default is the value we have so far, so an unset flag
	// leaves the env/default value in place. Secrets are deliberately NOT flags: command-line
	// arguments are visible to every user via `ps`.
	fs := flag.NewFlagSet("07config", flag.ContinueOnError)
	fs.SetOutput(usage)
	fs.StringVar(&cfg.Env, "env", cfg.Env, "environment: dev|staging|prod")
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address")
	fs.TextVar(&cfg.LogLevel, "log-level", &cfg.LogLevel, "log level: debug|info|warn|error")
	fs.DurationVar(&cfg.ReadTimeout, "read-timeout", cfg.ReadTimeout, "server read timeout")
	fs.IntVar(&cfg.MaxConns, "max-conns", cfg.MaxConns, "max connections")
	fs.BoolVar(&cfg.Serve, "serve", false, "run the real server until Ctrl-C")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	errs := env.errs
	errs = append(errs, cfg.validate()...)
	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", errors.Join(errs...))
	}
	return cfg, nil
}

func (c Config) validate() (errs []error) {
	if !slices.Contains([]string{"dev", "staging", "prod"}, c.Env) {
		errs = append(errs, fmt.Errorf("env: %q must be dev, staging or prod", c.Env))
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		errs = append(errs, fmt.Errorf("addr: %q is not host:port (%v)", c.Addr, err))
	}
	if c.ReadTimeout <= 0 || c.ReadTimeout > 5*time.Minute {
		errs = append(errs, fmt.Errorf("read timeout: %v must be in (0, 5m]", c.ReadTimeout))
	}
	if c.MaxConns < 1 || c.MaxConns > 1000 {
		errs = append(errs, fmt.Errorf("max conns: %d must be in [1, 1000]", c.MaxConns))
	}
	if c.Env == "prod" {
		if c.DBURL == "" {
			errs = append(errs, errors.New("APP_DB_URL is required in prod"))
		}
		if len(c.APIKey) < 16 {
			errs = append(errs, errors.New("APP_API_KEY is required in prod and must be at least 16 characters")) // never echo the value!
		}
	}
	return errs
}

// ---------- logger ----------

// newLogger: JSON in prod/staging (machines read it), text in dev (humans read it).
func newLogger(cfg Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.Env == "dev" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts)).With("env", cfg.Env) // attach env to every line
}

// ---------- application ----------

// Store is a stand-in dependency (think: database). It has its own state, guarded by its mutex.
type Store struct {
	mu   sync.Mutex
	hits int
}

func (s *Store) Hit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	return s.hits
}

// Application is the "dependency container": everything handlers need, passed explicitly.
// There is no global; two Applications with different config can coexist in one process.
type Application struct {
	cfg    Config
	logger *slog.Logger
	store  *Store
}

func newApplication(cfg Config, logger *slog.Logger, store *Store) *Application {
	return &Application{cfg: cfg, logger: logger, store: store}
}

func (app *Application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /hits", func(w http.ResponseWriter, r *http.Request) {
		n := app.store.Hit()
		app.logger.DebugContext(r.Context(), "hit counted", "hits", n)
		fmt.Fprintf(w, "hits=%d\n", n)
	})
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		// %+v on the struct: safe because Secret.String redacts. Printing the *raw* value is
		// impossible by accident.
		fmt.Fprintf(w, "%+v\n", app.cfg)
	})
	return mux
}

func (app *Application) server() *http.Server {
	return &http.Server{
		Addr:              app.cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: min(app.cfg.ReadTimeout, 5*time.Second),
		ReadTimeout:       app.cfg.ReadTimeout,
	}
}

// ---------- main ----------

func main() {
	cfg, err := loadConfig(os.Getenv, os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // a startup failure is printed plainly, then exit non-zero
		os.Exit(2)
	}
	if cfg.Serve {
		serve(cfg)
		return
	}
	demo()
}

func serve(cfg Config) {
	app := newApplication(cfg, newLogger(cfg, os.Stdout), &Store{})
	app.logger.Info("starting", "config", cfg) // secrets appear as [REDACTED]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := app.server()
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sc)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func demo() {
	try := func(label string, env map[string]string, args ...string) (Config, bool) {
		fmt.Printf("\n--- %s\n", label)
		cfg, err := loadConfig(fakeEnv(env), args, io.Discard)
		if err != nil {
			fmt.Println(err)
			return Config{}, false
		}
		fmt.Printf("ok: %+v\n", cfg)
		return cfg, true
	}

	try("defaults only", nil)
	try("env overrides defaults", map[string]string{"APP_ADDR": ":9000", "APP_READ_TIMEOUT": "750ms", "APP_LOG_LEVEL": "debug"})
	try("flags override env", map[string]string{"APP_ADDR": ":9000"}, "-addr", ":7000", "-max-conns", "5")
	try("many problems reported at once", map[string]string{
		"APP_ENV": "production", "APP_READ_TIMEOUT": "soon", "APP_MAX_CONNS": "lots", "APP_LOG_LEVEL": "loud"})
	try("prod without secrets", map[string]string{"APP_ENV": "prod"})
	prod, _ := try("valid prod: secrets are redacted when printed", map[string]string{
		"APP_ENV": "prod", "APP_DB_URL": "postgres://admin:hunter2@db/app", "APP_API_KEY": "0123456789abcdef"})

	fmt.Println("\n--- same config through slog: text handler (dev) and JSON handler (prod)")
	dev := defaultConfig()
	logDemo := func(cfg Config) {
		l := newLogger(cfg, os.Stdout)
		l.Info("config loaded", "config", cfg)
	}
	logDemo(dev)
	logDemo(prod)
	fmt.Println("(Reveal() is the only way to read it:", len(prod.DBURL.Reveal()), "bytes of real DB URL)")

	fmt.Println("\n--- two Applications with different config in one process: no globals")
	for _, name := range []string{":8001", ":8002"} {
		cfg := defaultConfig()
		cfg.Addr = name
		app := newApplication(cfg, newLogger(cfg, io.Discard), &Store{})
		ts := httptest.NewServer(app.routes())
		for range 2 {
			get(ts, "/hits")
		}
		fmt.Printf("app %s -> /hits says %s", name, get(ts, "/hits")) // each has its own Store
		ts.Close()
	}
}

func get(ts *httptest.Server, path string) string {
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
