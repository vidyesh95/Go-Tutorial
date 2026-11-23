// Use case 01: a command-line tool ("tasks") with only the standard library.
//
// Go is the language of command-line and infrastructure tooling: Docker, kubectl, Terraform,
// the GitHub CLI (gh), Hugo, Helm and Prometheus' promtool are all Go binaries. Reasons: one
// static binary with no runtime to install, trivial cross-compilation
// (GOOS=linux GOARCH=arm64 go build), and fast startup.
//
// What this lesson shows:
//   - Subcommands (add, list, done, rm) built on the stdlib `flag` package: one FlagSet per
//     subcommand, each with its own --help.
//   - run(args, stdout, stderr) int: the whole program as a plain function that RETURNS an
//     exit code. main() only calls os.Exit(run(...)). This is the trick that makes a CLI
//     testable (see main_test.go): tests pass buffers instead of the real terminal.
//   - Exit codes: 0 success, 1 runtime failure (file unreadable, task not found), 2 usage
//     error (unknown command, bad flag). Scripts rely on these, so be consistent.
//   - stdout vs stderr: results go to stdout (so `tasks list -json | jq` works), diagnostics
//     and errors go to stderr. Requested help (-h) is a result, so it goes to stdout.
//   - Reading stdin when it is piped:  printf 'a\nb\n' | tasks add   adds two tasks.
//   - Storage: one JSON file, written atomically (temp file + rename) so a crash mid-write
//     can never leave a half-written file behind.
//
// Gotchas:
//   - flag stops parsing at the first non-flag argument, so flags must come BEFORE
//     positional arguments: `tasks list -all`, not `tasks list x -all`.
//   - flag.ExitOnError calls os.Exit inside the library, which makes code untestable. Use
//     flag.ContinueOnError and handle the error yourself (we do).
//   - Never write to os.Stdout directly in logic. Take an io.Writer parameter instead.
//
// Try it:
//
//	go run ./08-use-cases/01cli                      # scripted demo against a temp file
//	go run ./08-use-cases/01cli add -h               # per-subcommand help
//	go run ./08-use-cases/01cli add "learn Go"       # real use; stores under os.UserConfigDir
//	go run ./08-use-cases/01cli -file /tmp/t.json list -json
//
// In industry: big CLIs (kubectl, gh, hugo, helm, docker) use spf13/cobra, and urfave/cli is the
// other popular choice. They add nested subcommands, shell completion, generated docs,
// POSIX-style --long/-s flags and config-file/env binding. The stdlib `flag` package is enough
// when you have a handful of subcommands and flat flags, which covers most internal tools
// and one-off utilities. Reach for cobra when you need nested command trees or completion.
// The shape you learn here (run() returning an int, injected writers) is the same in either.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Exit codes. Named constants beat magic numbers in os.Exit calls.
const (
	exitOK      = 0
	exitFailure = 1 // the command was valid but failed
	exitUsage   = 2 // the command line itself was wrong
)

// Task is one stored item. JSON tags define the on-disk format.
type Task struct {
	ID      int       `json:"id"`
	Title   string    `json:"title"`
	Done    bool      `json:"done"`
	Created time.Time `json:"created"`
}

// store persists tasks in a single JSON file.
type store struct{ path string }

func (s store) load() ([]Task, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // first run: no file yet means no tasks, not an error
	}
	if err != nil {
		return nil, err
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", s.path, err)
	}
	return tasks, nil
}

// save writes atomically: write a temp file in the SAME directory, then rename it over the
// target. Rename is atomic on POSIX filesystems, so readers see the old or the new file, never half.
func (s store) save(tasks []Task) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tasks-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // harmless after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// defaultPath picks the data file: $TASKS_FILE, else <user config dir>/go-tutorial/tasks.json.
// os.UserConfigDir is ~/.config on Linux, ~/Library/Application Support on macOS, %AppData% on Windows.
func defaultPath() string {
	if p := os.Getenv("TASKS_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "go-tutorial", "tasks.json")
}

func main() {
	if len(os.Args) == 1 {
		runDemo()
		return
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It reads the real stdin; tests use runIO to inject their own.
func run(args []string, stdout, stderr io.Writer) int {
	return runIO(args, os.Stdin, stdout, stderr)
}

const globalUsage = `tasks - a tiny task manager

Usage:
  tasks [-file path] <command> [flags] [args]

Commands:
  add [title...]   add a task (titles are read from stdin lines when input is piped)
  list             show tasks (-all, -json)
  done <id>...     mark tasks as done
  rm <id>...       delete tasks

Run "tasks <command> -h" for help on one command.

Global flags:
  -file path   JSON data file (default: $TASKS_FILE, else <user config dir>/go-tutorial/tasks.json)
`

func runIO(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("tasks", flag.ContinueOnError)
	file := global.String("file", defaultPath(), "path of the JSON data file")
	global.SetOutput(io.Discard) // we print errors and usage ourselves, below
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, globalUsage)
			return exitOK
		}
		fmt.Fprintf(stderr, "tasks: %v\n\n%s", err, globalUsage)
		return exitUsage
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(stderr, globalUsage)
		return exitUsage
	}

	st := store{path: *file}
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "add":
		return cmdAdd(st, cmdArgs, stdin, stdout, stderr)
	case "list":
		return cmdList(st, cmdArgs, stdout, stderr)
	case "done":
		return cmdDoneOrRm(st, "done", cmdArgs, stdout, stderr)
	case "rm":
		return cmdDoneOrRm(st, "rm", cmdArgs, stdout, stderr)
	case "help":
		fmt.Fprint(stdout, globalUsage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "tasks: unknown command %q\n\n%s", cmd, globalUsage)
		return exitUsage
	}
}

// parseFlags parses and reports. ok=false means the caller should return code immediately.
func parseFlags(fs *flag.FlagSet, synopsis string, args []string, stdout, stderr io.Writer) (code int, ok bool) {
	err := fs.Parse(args)
	switch {
	case err == nil:
		return 0, true
	case errors.Is(err, flag.ErrHelp):
		// The user ASKED for help, so it is normal output: stdout, exit 0.
		fmt.Fprintf(stdout, "Usage: %s\n", synopsis)
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return exitOK, false
	default:
		fmt.Fprintf(stderr, "tasks %s: %v\nUsage: %s\n", fs.Name(), err, synopsis)
		return exitUsage, false
	}
}

func cmdAdd(st store, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	const synopsis = "tasks add [title...]   (or pipe titles on stdin, one per line)"
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	quiet := fs.Bool("q", false, "quiet: print only the new task IDs")
	if code, ok := parseFlags(fs, synopsis, args, stdout, stderr); !ok {
		return code
	}

	var titles []string
	if fs.NArg() > 0 {
		titles = []string{strings.Join(fs.Args(), " ")}
	} else if isPipe(stdin) {
		// Piped input: one task per non-empty line.
		data, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "tasks add: reading stdin: %v\n", err)
			return exitFailure
		}
		for line := range strings.Lines(string(data)) {
			if t := strings.TrimSpace(line); t != "" {
				titles = append(titles, t)
			}
		}
	}
	if len(titles) == 0 {
		fmt.Fprintf(stderr, "tasks add: no title given\nUsage: %s\n", synopsis)
		return exitUsage
	}

	tasks, err := st.load()
	if err != nil {
		fmt.Fprintf(stderr, "tasks add: %v\n", err)
		return exitFailure
	}
	next := nextID(tasks)
	for _, title := range titles {
		tasks = append(tasks, Task{ID: next, Title: title, Created: time.Now().UTC().Truncate(time.Second)})
		if *quiet {
			fmt.Fprintln(stdout, next)
		} else {
			fmt.Fprintf(stdout, "added #%d %s\n", next, title)
		}
		next++
	}
	if err := st.save(tasks); err != nil {
		fmt.Fprintf(stderr, "tasks add: %v\n", err)
		return exitFailure
	}
	return exitOK
}

func cmdList(st store, args []string, stdout, stderr io.Writer) int {
	const synopsis = "tasks list [-all] [-json]"
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	all := fs.Bool("all", false, "include completed tasks")
	asJSON := fs.Bool("json", false, "machine-readable JSON output")
	if code, ok := parseFlags(fs, synopsis, args, stdout, stderr); !ok {
		return code
	}
	tasks, err := st.load()
	if err != nil {
		fmt.Fprintf(stderr, "tasks list: %v\n", err)
		return exitFailure
	}
	var shown []Task
	for _, t := range tasks {
		if *all || !t.Done {
			shown = append(shown, t)
		}
	}
	if *asJSON {
		if shown == nil {
			shown = []Task{} // encode as [] rather than null
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(shown); err != nil {
			fmt.Fprintf(stderr, "tasks list: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	if len(shown) == 0 {
		// A status message, not data: stderr keeps stdout clean for pipes.
		fmt.Fprintln(stderr, "no tasks")
		return exitOK
	}
	for _, t := range shown {
		mark := " "
		if t.Done {
			mark = "x"
		}
		fmt.Fprintf(stdout, "[%s] #%d %s\n", mark, t.ID, t.Title)
	}
	return exitOK
}

// cmdDoneOrRm shares one implementation because both commands are "apply an action to IDs".
func cmdDoneOrRm(st store, name string, args []string, stdout, stderr io.Writer) int {
	synopsis := "tasks " + name + " <id>..."
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if code, ok := parseFlags(fs, synopsis, args, stdout, stderr); !ok {
		return code
	}
	if fs.NArg() == 0 {
		fmt.Fprintf(stderr, "tasks %s: need at least one id\nUsage: %s\n", name, synopsis)
		return exitUsage
	}
	ids := make([]int, 0, fs.NArg())
	for _, a := range fs.Args() {
		id, err := strconv.Atoi(strings.TrimPrefix(a, "#"))
		if err != nil {
			fmt.Fprintf(stderr, "tasks %s: %q is not a task id\n", name, a)
			return exitUsage
		}
		ids = append(ids, id)
	}

	tasks, err := st.load()
	if err != nil {
		fmt.Fprintf(stderr, "tasks %s: %v\n", name, err)
		return exitFailure
	}
	// Validate everything first so a typo in the 3rd id does not leave a half-applied change.
	for _, id := range ids {
		if !slices.ContainsFunc(tasks, func(t Task) bool { return t.ID == id }) {
			fmt.Fprintf(stderr, "tasks %s: no task with id %d\n", name, id)
			return exitFailure
		}
	}
	for _, id := range ids {
		if name == "rm" {
			tasks = slices.DeleteFunc(tasks, func(t Task) bool { return t.ID == id })
			fmt.Fprintf(stdout, "removed #%d\n", id)
			continue
		}
		for i := range tasks {
			if tasks[i].ID == id {
				tasks[i].Done = true
			}
		}
		fmt.Fprintf(stdout, "completed #%d\n", id)
	}
	if err := st.save(tasks); err != nil {
		fmt.Fprintf(stderr, "tasks %s: %v\n", name, err)
		return exitFailure
	}
	return exitOK
}

func nextID(tasks []Task) int {
	next := 1
	for _, t := range tasks {
		next = max(next, t.ID+1)
	}
	return next
}

// isPipe reports whether r looks like piped/redirected input rather than an interactive
// terminal. On a terminal the user would be left staring at a blocked read, so we only read
// stdin when it is NOT a character device. Non-file readers (tests) count as piped.
func isPipe(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice == 0
}

// runDemo scripts a session against a temp file so that `go run` with no arguments shows
// every feature, including the error paths, and cleans up after itself.
func runDemo() {
	dir, err := os.MkdirTemp("", "tasks-demo-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitFailure)
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "tasks.json")
	fmt.Println("Scripted demo (data file:", file+")")
	fmt.Println("Pass any argument to use the real CLI, e.g. `go run ./08-use-cases/01cli list`.")

	steps := []struct {
		stdin string
		args  []string
	}{
		{args: []string{"add", "learn", "Go", "concurrency"}},
		{args: []string{"add", "-q", "write", "a", "CLI"}},
		{stdin: "buy milk\n\nread the Go blog\n", args: []string{"add"}}, // piped stdin
		{args: []string{"list"}},
		{args: []string{"done", "1", "#3"}},
		{args: []string{"list"}},
		{args: []string{"list", "-all"}},
		{args: []string{"list", "-json", "-all"}},
		{args: []string{"rm", "2"}},
		{args: []string{"done", "99"}},    // runtime failure -> exit 1
		{args: []string{"frobnicate"}},    // usage error -> exit 2
		{args: []string{"list", "-nope"}}, // bad flag -> exit 2
		{args: []string{"add", "-h"}},     // help -> stdout, exit 0
		{args: []string{"done"}},          // missing argument -> exit 2
		{args: []string{"list", "-all"}},  // final state
	}
	for _, s := range steps {
		full := append([]string{"-file", file}, s.args...)
		display := "tasks " + strings.Join(s.args, " ")
		if s.stdin != "" {
			display = fmt.Sprintf("printf %q | %s", s.stdin, display)
		}
		fmt.Printf("\n$ %s\n", display)
		code := runIO(full, strings.NewReader(s.stdin), prefixWriter{os.Stdout, "  "}, prefixWriter{os.Stdout, "  [stderr] "})
		fmt.Printf("  (exit %d)\n", code)
	}
}

// prefixWriter indents each line so the demo output shows which stream a line came from.
// It assumes each Write is whole lines, which holds for the fmt.Fprint calls above.
type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	for line := range strings.Lines(string(b)) {
		if _, err := fmt.Fprint(p.w, p.prefix+line); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}
