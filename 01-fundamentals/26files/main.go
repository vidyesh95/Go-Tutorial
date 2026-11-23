// Lesson 26: Working with files
//
// Quick reference:
//   - os.ReadFile / os.WriteFile   whole-file helpers (fine for small files)
//   - os.Create / os.Open          get an *os.File; ALWAYS `defer f.Close()` after the error check
//   - bufio.Scanner                read line by line without loading the whole file
//   - io.Copy                      stream from any io.Reader to any io.Writer (constant memory)
//   - filepath.Join                build paths portably (never concatenate with "/")
//   - filepath.WalkDir             walk a directory tree
//   - os.MkdirTemp / t.TempDir     scratch directories; remove them when done
//   - os.Root (Go 1.24)            a directory "jail": paths that escape it (../, symlinks) are rejected
//   - embed.FS                     ship files inside the compiled binary
//
// Key rules and gotchas:
//   - os.File implements io.Reader and io.Writer; that is why io.Copy and bufio work on it.
//   - Errors: use errors.Is(err, fs.ErrNotExist) to test "file missing".
//   - bufio.Scanner has a 64KB per-line limit by default; call scanner.Buffer for longer lines
//     and ALWAYS check scanner.Err() after the loop.
//   - Writing: Close() can return the final write error. For files you write, check it.
//   - Path traversal: if a path comes from a user ("../../etc/passwd"), filepath.Join alone
//     does NOT protect you. Use os.Root (or filepath.IsLocal) to confine access.
//   - //go:embed needs the "embed" import; the pattern is relative to this source file and
//     cannot use "..".
//
// In industry: backend services mostly stream (io.Copy, bufio) instead of reading big
// files into memory, use os.Root or object storage (S3) for user-supplied file names,
// and embed.FS to ship SQL migrations, HTML templates and static assets inside a single
// binary that deploys as one file.
package main

import (
	"bufio"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed data/greeting.txt
var greeting string // a single file can be embedded straight into a string

//go:embed data
var dataFS embed.FS // a whole directory as a read-only file system

func main() {
	// All writes happen in a throwaway directory so the repo stays clean.
	dir, err := os.MkdirTemp("", "go-lesson-files-*")
	if err != nil {
		fmt.Println("MkdirTemp:", err)
		return
	}
	defer os.RemoveAll(dir) // clean up everything at the end
	fmt.Println("temp dir created (and removed at exit)")

	fmt.Println("\n--- WriteFile / ReadFile ---")
	notes := filepath.Join(dir, "notes.txt")
	// 0o644 = owner read/write, others read. 0o600 for secrets.
	if err := os.WriteFile(notes, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		fmt.Println("WriteFile:", err)
		return
	}
	b, err := os.ReadFile(notes)
	fmt.Printf("ReadFile: %q err=%v\n", b, err)

	_, err = os.ReadFile(filepath.Join(dir, "missing.txt"))
	fmt.Println("missing file is ErrNotExist:", errors.Is(err, fs.ErrNotExist))

	fmt.Println("\n--- Create + defer Close, then bufio.Scanner ---")
	if err := writeLines(filepath.Join(dir, "out.txt"), []string{"one", "two", "three"}); err != nil {
		fmt.Println("writeLines:", err)
		return
	}
	if err := printLines(filepath.Join(dir, "out.txt")); err != nil {
		fmt.Println("printLines:", err)
		return
	}

	fmt.Println("\n--- io.Copy (streams, constant memory) ---")
	src, err := os.Open(notes)
	if err != nil {
		fmt.Println("Open:", err)
		return
	}
	defer src.Close()
	var sb strings.Builder // any io.Writer works as the destination
	n, err := io.Copy(&sb, src)
	fmt.Printf("copied %d bytes, err=%v\n", n, err)

	fmt.Println("\n--- filepath.Join and WalkDir ---")
	sub := filepath.Join(dir, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		fmt.Println("MkdirAll:", err)
		return
	}
	if err := os.WriteFile(filepath.Join(sub, "x.txt"), []byte("x"), 0o644); err != nil {
		fmt.Println("WriteFile:", err)
		return
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path) // show paths relative to the temp dir
		kind := "file"
		if d.IsDir() {
			kind = "dir "
		}
		fmt.Println(" ", kind, rel)
		return nil
	})
	if err != nil {
		fmt.Println("WalkDir:", err)
	}

	fmt.Println("\n--- os.Root: blocks path traversal ---")
	demoRoot(dir)

	fmt.Println("\n--- embed.FS ---")
	fmt.Printf("embedded string: %q\n", greeting)
	entries, _ := fs.ReadDir(dataFS, "data")
	for _, e := range entries {
		fmt.Println("embedded file:", e.Name())
	}
	content, _ := dataFS.ReadFile("data/greeting.txt") // paths always use forward slashes
	fmt.Println("same content via FS:", string(content) == greeting)
}

// writeLines shows the create/close pattern, including checking Close's error.
func writeLines(path string, lines []string) (err error) {
	f, err := os.Create(path) // truncates if it exists
	if err != nil {
		return err
	}
	defer func() {
		// Close can fail (e.g. disk full on flush). Keep the first error.
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	w := bufio.NewWriter(f) // buffer small writes
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return w.Flush()
}

// printLines reads a file one line at a time.
func printLines(path string) error {
	f, err := os.Open(path) // read-only
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		fmt.Printf("  %d: %s\n", lineNo, sc.Text())
	}
	return sc.Err() // do not forget this
}

// demoRoot shows that os.Root confines all access to one directory tree.
func demoRoot(dir string) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		fmt.Println("OpenRoot:", err)
		return
	}
	defer root.Close()

	// Inside the root: fine.
	data, err := root.ReadFile("notes.txt")
	fmt.Printf("inside root : %q err=%v\n", data, err)

	// A user-supplied name trying to escape: rejected, no matter how it is spelled.
	for _, evil := range []string{"../etc/passwd", "sub/../../secret", "/etc/passwd"} {
		_, err := root.ReadFile(evil)
		fmt.Printf("%-18s -> rejected: %v\n", evil, err != nil)
	}
	_, err = root.ReadFile("../etc/passwd")
	fmt.Println("error text:", err)
}
