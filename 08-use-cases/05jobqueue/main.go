// Use case 05: a background job queue backed by SQLite.
//
// Web requests must be fast, so slow or unreliable work (send an email, resize an image, call a
// flaky partner API, generate a report) is written to a queue and processed by background
// WORKERS. A queue also survives restarts: jobs live in a database table, not in memory.
//
// Table: jobs(id, kind, payload JSON, status, attempts, max_attempts, run_at, last_error, locked_until)
//
//	status:  queued --claim--> running --ok--> succeeded
//	                              |--fail, attempts < max--> queued (run_at pushed into the future)
//	                              '--fail, attempts >= max--> dead      (dead-letter: kept for humans)
//
// Key ideas, each marked in the code:
//   - ATOMIC CLAIM. N workers poll the same table; each job must go to exactly one of them. One
//     `UPDATE ... WHERE id = (SELECT ... LIMIT 1) RETURNING ...` both picks and locks a row in a
//     single statement inside a transaction, so two workers can never get the same job.
//     In PostgreSQL you write `SELECT ... FOR UPDATE SKIP LOCKED` instead, which lets many
//     workers grab DIFFERENT rows in parallel without waiting on each other. SQLite has a single
//     writer at a time, which is fine for a lesson and small apps.
//   - RETRIES WITH EXPONENTIAL BACKOFF. A failed job is re-queued with run_at = now + base*2^(n-1)
//     so a struggling dependency is not hammered. Production adds random jitter so thousands of
//     failed jobs do not all retry at the same instant (thundering herd).
//   - DEAD LETTER. After max_attempts the job becomes 'dead' with last_error, so a poison job
//     cannot loop forever and operators can inspect or replay it. ErrPermanent skips retries.
//   - VISIBILITY TIMEOUT. A claim sets locked_until = now + timeout. If a worker crashes
//     (kill -9, OOM, deploy) the row stays 'running'; a reaper goroutine notices
//     locked_until < now and puts it back in the queue. SQS calls this the "visibility timeout".
//   - FENCING. Completing a job is `... WHERE id = ? AND status = 'running' AND attempts = ?`.
//     If a slow worker lost its lease and another worker re-claimed the job, the late worker's
//     result is ignored instead of clobbering the newer attempt.
//   - GRACEFUL SHUTDOWN. When the context is cancelled, workers stop CLAIMING but finish the job
//     in hand (its handler context is detached from cancellation via context.WithoutCancel,
//     with its own timeout); Run returns only after every in-flight job has been recorded.
//
// DELIVERY IS AT-LEAST-ONCE. A worker can finish the side effect (email sent) and crash before
// recording success, so the job runs again. Therefore HANDLERS MUST BE IDEMPOTENT: use an
// idempotency key (e.g. the payment provider's key, a unique constraint, "INSERT ... ON CONFLICT
// DO NOTHING") so running twice has the effect of running once. Exactly-once does not exist
// across systems; at-least-once + idempotency is the practical answer.
//
// SQLite notes: WAL mode + busy_timeout let several connections cooperate; _txlock=immediate
// makes every transaction take the write lock at BEGIN, avoiding "database is locked"
// surprises when a read transaction tries to upgrade. Times are unix milliseconds (integers).
//
// Try it:
//
//	go run ./08-use-cases/05jobqueue                       # demo with a temp database, exits
//	go run ./08-use-cases/05jobqueue -v                    # also show retry / dead-letter log lines
//	go run ./08-use-cases/05jobqueue -db /tmp/q.db -enqueue 10    # add jobs to a real file
//	go run ./08-use-cases/05jobqueue -db /tmp/q.db -work          # run workers; Ctrl-C = graceful,
//	                                                              # `kill -9` then restart = recovery
//
// In industry: Go teams use River (Postgres-backed, transactional enqueue, SKIP LOCKED),
// Asynq or Machinery (Redis-backed, like Ruby's Sidekiq or Python's Celery), Temporal for
// long-running workflows, or cloud queues (SQS, Pub/Sub) with a Go consumer. Writing your own on
// a database table is common for modest volume because you can enqueue in the SAME transaction as
// your business data ("transactional outbox"), which a separate broker cannot give you.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"text/tabwriter"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    payload      TEXT    NOT NULL DEFAULT '{}',
    status       TEXT    NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','dead')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    run_at       INTEGER NOT NULL,   -- unix ms: do not start before this time
    last_error   TEXT,
    locked_until INTEGER,            -- unix ms: lease expiry while status = 'running'
    created_at   INTEGER NOT NULL
);
-- The claim query filters on status and run_at and orders by run_at: index exactly that.
CREATE INDEX IF NOT EXISTS jobs_claim_idx ON jobs (status, run_at);
`

// openDB opens (creating if needed) a SQLite file with settings suited to a multi-worker queue.
// Same idea as the shared helper in the SQL track (03-sql-sqlite/internal/sqldb): pragmas live in
// the DSN so EVERY pooled connection gets them. A lesson cannot import another track's internal
// package, so this is a small copy.
func openDB(ctx context.Context, path string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

// Job is what a handler receives.
type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Attempts    int // includes the current attempt (1 on the first run)
	MaxAttempts int
}

// Handler does the work. Return nil for success, an error to retry, or an error wrapping
// ErrPermanent to give up immediately.
type Handler func(ctx context.Context, job Job) error

// ErrPermanent marks failures that retrying cannot fix (bad payload, unknown user).
var ErrPermanent = errors.New("permanent failure")

// Queue is the job store plus the worker pool.
type Queue struct {
	db       *sql.DB
	log      *slog.Logger
	handlers map[string]Handler

	visibility time.Duration // lease length: how long a claimed job may run before it is presumed lost
	jobTimeout time.Duration // per-attempt deadline given to handlers (should be < visibility)
	poll       time.Duration // idle polling interval
	backoff    time.Duration // base retry delay
	reapEvery  time.Duration
}

func NewQueue(db *sql.DB, log *slog.Logger) *Queue {
	return &Queue{
		db: db, log: log, handlers: make(map[string]Handler),
		visibility: 30 * time.Second, jobTimeout: 20 * time.Second,
		poll: 500 * time.Millisecond, backoff: 2 * time.Second, reapEvery: 5 * time.Second,
	}
}

// Register attaches a handler to a job kind. Call before Run.
func (q *Queue) Register(kind string, h Handler) { q.handlers[kind] = h }

type enqueueOpts struct {
	maxAttempts int
	delay       time.Duration
}

// EnqueueOption customises one job (functional options: the idiomatic Go way to add optional
// parameters without a huge signature).
type EnqueueOption func(*enqueueOpts)

func WithMaxAttempts(n int) EnqueueOption     { return func(o *enqueueOpts) { o.maxAttempts = n } }
func WithDelay(d time.Duration) EnqueueOption { return func(o *enqueueOpts) { o.delay = d } }

func nowMS() int64 { return time.Now().UnixMilli() }

// Enqueue stores a job and returns its ID. Pass a *sql.Tx-based variant in a real app so the job
// commits atomically with your business data.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any, opts ...EnqueueOption) (int64, error) {
	o := enqueueOpts{maxAttempts: 3}
	for _, opt := range opts {
		opt(&o)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	now := nowMS()
	res, err := q.db.ExecContext(ctx,
		`INSERT INTO jobs (kind, payload, max_attempts, run_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		kind, string(data), o.maxAttempts, now+o.delay.Milliseconds(), now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// claim atomically takes the oldest due job, or returns ok=false if there is none.
//
// One statement picks the row (subquery) AND marks it running AND returns it. Because it runs
// inside a transaction holding SQLite's write lock, two workers cannot both match the same row.
// attempts is incremented at claim time, so a crash mid-job still counts as an attempt.
func (q *Queue) claim(ctx context.Context, lease time.Duration) (job Job, ok bool, err error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback() // no-op after Commit

	now := nowMS()
	var payload string
	err = tx.QueryRowContext(ctx, `
		UPDATE jobs
		   SET status = 'running', attempts = attempts + 1, locked_until = ?
		 WHERE id = (SELECT id FROM jobs
		              WHERE status = 'queued' AND run_at <= ?
		              ORDER BY run_at, id
		              LIMIT 1)
		RETURNING id, kind, payload, attempts, max_attempts`,
		now+lease.Milliseconds(), now,
	).Scan(&job.ID, &job.Kind, &payload, &job.Attempts, &job.MaxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil // nothing due; the deferred Rollback releases the lock
	}
	if err != nil {
		return Job{}, false, err
	}
	job.Payload = json.RawMessage(payload)
	return job, true, tx.Commit()
}

// complete records success. The attempts check is the fencing token (see header comment).
func (q *Queue) complete(ctx context.Context, job Job) error {
	res, err := q.db.ExecContext(ctx,
		`UPDATE jobs SET status='succeeded', locked_until=NULL, last_error=NULL
		  WHERE id=? AND status='running' AND attempts=?`, job.ID, job.Attempts)
	return q.checkFenced(res, err, job)
}

// fail either schedules a retry with exponential backoff or dead-letters the job.
func (q *Queue) fail(ctx context.Context, job Job, cause error) error {
	if errors.Is(cause, ErrPermanent) || job.Attempts >= job.MaxAttempts {
		q.log.Debug("job dead-lettered", "id", job.ID, "kind", job.Kind, "attempts", job.Attempts, "err", cause)
		res, err := q.db.ExecContext(ctx,
			`UPDATE jobs SET status='dead', locked_until=NULL, last_error=?
			  WHERE id=? AND status='running' AND attempts=?`, cause.Error(), job.ID, job.Attempts)
		return q.checkFenced(res, err, job)
	}
	delay := retryDelay(q.backoff, job.Attempts)
	q.log.Debug("job failed, will retry", "id", job.ID, "kind", job.Kind, "attempt", job.Attempts, "retry_in", delay, "err", cause)
	res, err := q.db.ExecContext(ctx,
		`UPDATE jobs SET status='queued', locked_until=NULL, last_error=?, run_at=?
		  WHERE id=? AND status='running' AND attempts=?`, cause.Error(), nowMS()+delay.Milliseconds(), job.ID, job.Attempts)
	return q.checkFenced(res, err, job)
}

func (q *Queue) checkFenced(res sql.Result, err error, job Job) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Our lease expired and someone else owns the job now (or it was reaped). Do not overwrite.
		q.log.Warn("lost lease; result ignored", "id", job.ID, "attempt", job.Attempts)
	}
	return nil
}

// retryDelay is base * 2^(attempt-1): base, 2*base, 4*base ... capped so it never overflows.
func retryDelay(base time.Duration, attempt int) time.Duration {
	shift := min(max(attempt-1, 0), 16)
	return base << shift
}

// reap re-queues jobs whose lease expired (their worker presumably crashed) or dead-letters
// them if they were already on their last attempt. It returns how many rows it touched.
func (q *Queue) reap(ctx context.Context) (int64, error) {
	res, err := q.db.ExecContext(ctx, `
		UPDATE jobs
		   SET status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'queued' END,
		       last_error = 'worker lost: lease expired',
		       locked_until = NULL,
		       run_at = ?
		 WHERE status = 'running' AND locked_until < ?`, nowMS(), nowMS())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		q.log.Debug("reaped expired leases", "jobs", n)
	}
	return n, nil
}

// Run starts `workers` workers plus a reaper and blocks until ctx is cancelled AND every
// in-flight job has been recorded. Cancelling ctx means "stop taking new work", not "abort
// what you are doing".
func (q *Queue) Run(ctx context.Context, workers int) {
	var wg sync.WaitGroup
	wg.Go(func() { q.reaper(ctx) })
	for id := range workers {
		wg.Go(func() { q.worker(ctx, id) })
	}
	wg.Wait()
}

func (q *Queue) reaper(ctx context.Context) {
	t := time.NewTicker(q.reapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := q.reap(ctx); err != nil && ctx.Err() == nil {
				q.log.Error("reap failed", "err", err)
			}
		}
	}
}

func (q *Queue) worker(ctx context.Context, id int) {
	for ctx.Err() == nil {
		job, ok, err := q.claim(ctx, q.visibility)
		if err != nil {
			if ctx.Err() == nil {
				q.log.Error("claim failed", "worker", id, "err", err)
			}
		}
		if err != nil || !ok {
			select { // idle: wait for the next poll, but wake immediately on shutdown
			case <-ctx.Done():
				return
			case <-time.After(q.poll):
			}
			continue
		}
		q.process(ctx, id, job)
	}
}

// process runs one claimed job to completion, even if ctx gets cancelled meanwhile.
func (q *Queue) process(ctx context.Context, worker int, job Job) {
	// Detach from shutdown cancellation: the job gets its own timeout instead.
	jctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), q.jobTimeout)
	defer cancel()

	err := q.run(jctx, job)
	// Recording the outcome also must not be cancelled by shutdown, or the job would stay
	// 'running' until its lease expires.
	wctx := context.WithoutCancel(ctx)
	if err != nil {
		err = q.fail(wctx, job, err)
	} else {
		err = q.complete(wctx, job)
	}
	if err != nil {
		q.log.Error("could not record job outcome", "id", job.ID, "err", err)
	}
}

// run looks up the handler and converts panics into errors: one buggy job must not kill the worker.
func (q *Queue) run(ctx context.Context, job Job) (err error) {
	h, ok := q.handlers[job.Kind]
	if !ok {
		return fmt.Errorf("%w: no handler for kind %q", ErrPermanent, job.Kind)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return h(ctx, job)
}

// Pending counts jobs that are not finished (queued or running).
func (q *Queue) Pending(ctx context.Context) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running')`).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------------------------------
// Handlers used by the demo. Notice they only depend on the job, never on global state.

type emailPayload struct {
	To        string `json:"to"`
	FailTimes int    `json:"fail_times"` // simulate a flaky SMTP server: fail this many attempts first
}

func sendEmail(_ context.Context, job Job) error {
	var p emailPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: bad payload: %v", ErrPermanent, err) // retrying a bad payload is pointless
	}
	if job.Attempts <= p.FailTimes {
		return fmt.Errorf("smtp 451: temporary failure sending to %s", p.To)
	}
	// IDEMPOTENCY: a real handler would send with an idempotency key such as
	// fmt.Sprintf("welcome-email-%d", userID) so a re-run cannot send two emails.
	return nil
}

func resizeImage(_ context.Context, job Job) error {
	time.Sleep(5 * time.Millisecond) // pretend to work
	return nil
}

func generateReport(_ context.Context, job Job) error {
	return errors.New("upstream reporting API returned 503") // always fails: ends in the dead-letter state
}

func slowJob(ctx context.Context, job Job) error {
	select {
	case <-time.After(200 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func registerHandlers(q *Queue) {
	q.Register("send_email", sendEmail)
	q.Register("resize_image", resizeImage)
	q.Register("generate_report", generateReport)
	q.Register("slow", slowJob)
}

// ---------------------------------------------------------------------------------------

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func runDemo(verbose bool) error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "jobqueue-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := openDB(ctx, filepath.Join(dir, "jobs.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	q := NewQueue(db, newLogger(verbose))
	// Short timings so the whole demo finishes in about a second. Production values are in NewQueue.
	q.visibility, q.poll, q.backoff, q.reapEvery = 150*time.Millisecond, 5*time.Millisecond, 15*time.Millisecond, 20*time.Millisecond
	registerHandlers(q)

	// 20 jobs of different personalities.
	var ids []int64
	add := func(kind string, payload any, opts ...EnqueueOption) int64 {
		id, err := q.Enqueue(ctx, kind, payload, opts...)
		if err != nil {
			panic(err) // demo only
		}
		ids = append(ids, id)
		return id
	}
	// Job #1 is the victim of a "crashed worker": it gets claimed and then abandoned (below).
	crashed := add("resize_image", map[string]any{"file": "crashed-worker.jpg"})
	// Simulate a worker that claims the job and then dies without reporting back. It leases the
	// job for only 30ms; no handler ever runs and nothing completes it.
	if _, ok, err := q.claim(ctx, 30*time.Millisecond); err != nil || !ok {
		return fmt.Errorf("simulated crash claim: ok=%v err=%v", ok, err)
	}
	fmt.Println("simulated crash: a worker claimed a job and vanished (the reaper will recover it)")

	for i := range 9 {
		add("send_email", emailPayload{To: fmt.Sprintf("user%d@example.com", i)}) // healthy
	}
	for i := range 3 {
		add("send_email", emailPayload{To: fmt.Sprintf("flaky%d@example.com", i), FailTimes: 1 + i%2}, WithMaxAttempts(4)) // transient failures
	}
	for i := range 4 {
		add("resize_image", map[string]any{"file": fmt.Sprintf("photo%d.jpg", i)})
	}
	add("generate_report", map[string]any{"month": "2026-10"}, WithMaxAttempts(3))                // always fails -> dead
	add("send_email", json.RawMessage(`"this is not an object"`))                                 // bad payload -> permanent -> dead at once
	add("resize_image", map[string]any{"file": "scheduled.jpg"}, WithDelay(100*time.Millisecond)) // runs later
	fmt.Printf("enqueued %d jobs\n", len(ids))

	// Start 4 workers and wait until nothing is pending.
	runCtx, stop := context.WithCancel(ctx)
	defer stop() // also called explicitly below for the shutdown demo
	runDone := make(chan struct{})
	go func() { defer close(runDone); q.Run(runCtx, 4) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		n, err := q.Pending(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the queue to drain")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Graceful shutdown demo: cancel while a slow job is mid-flight.
	slowID := add("slow", nil)
	for { // wait until a worker has actually started it
		var status string
		if err := db.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id=?`, slowID).Scan(&status); err != nil {
			return err
		}
		if status == "running" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	fmt.Println("cancelling workers while the 200ms 'slow' job is running...")
	stop()
	<-runDone // Run returns only after the in-flight job was recorded
	fmt.Println("workers stopped; in-flight job was allowed to finish")

	return printOutcome(ctx, db, crashed)
}

func printOutcome(ctx context.Context, db *sql.DB, crashedID int64) error {
	rows, err := db.QueryContext(ctx, `SELECT id, kind, status, attempts, max_attempts, COALESCE(last_error,'') FROM jobs ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nID\tKIND\tSTATUS\tATTEMPTS\tLAST ERROR / NOTE")
	counts := map[string]int{}
	for rows.Next() {
		var (
			id            int64
			kind, status  string
			attempts, max int
			lastErr       string
		)
		if err := rows.Scan(&id, &kind, &status, &attempts, &max, &lastErr); err != nil {
			return err
		}
		counts[status]++
		note := lastErr
		if len(note) > 60 {
			note = note[:57] + "..."
		}
		if id == crashedID {
			note = "(recovered after its worker crashed)"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d/%d\t%s\n", id, kind, status, attempts, max, note)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tw.Flush()
	fmt.Printf("\nsummary: %d succeeded, %d dead-lettered, %d still queued/running\n",
		counts["succeeded"], counts["dead"], counts["queued"]+counts["running"])
	return nil
}

func main() {
	dbPath := flag.String("db", "", "SQLite file for real use (default: temp database + scripted demo)")
	enqueue := flag.Int("enqueue", 0, "with -db: add N send_email jobs and exit")
	work := flag.Bool("work", false, "with -db: run workers until Ctrl-C")
	workers := flag.Int("workers", 4, "with -work: number of workers")
	verbose := flag.Bool("v", false, "debug logging (retries, dead letters, reaping)")
	flag.Parse()

	if *dbPath == "" {
		if err := runDemo(*verbose); err != nil {
			fmt.Fprintln(os.Stderr, "demo failed:", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	db, err := openDB(ctx, *dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	q := NewQueue(db, newLogger(*verbose))
	registerHandlers(q)

	switch {
	case *enqueue > 0:
		for i := range *enqueue {
			id, err := q.Enqueue(ctx, "send_email", emailPayload{To: fmt.Sprintf("user%d@example.com", i), FailTimes: i % 3})
			if err != nil {
				fmt.Fprintln(os.Stderr, "enqueue:", err)
				os.Exit(1)
			}
			fmt.Println("enqueued job", id)
		}
	case *work:
		fmt.Printf("running %d workers on %s (Ctrl-C to stop gracefully)\n", *workers, *dbPath)
		q.Run(ctx, *workers)
		fmt.Println("stopped")
	default:
		fmt.Fprintln(os.Stderr, "with -db, pass -enqueue N or -work")
		os.Exit(2)
	}
}
