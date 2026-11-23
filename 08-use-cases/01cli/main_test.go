package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Because run takes writers and returns the exit code, a CLI test needs no subprocess and no
// os.Exit trickery: call it, then assert on the code and on what went to each stream.
func TestRun(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")

	// call runs one invocation against the shared data file and returns code, stdout, stderr.
	call := func(stdin string, args ...string) (int, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := runIO(append([]string{"-file", file}, args...), strings.NewReader(stdin), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	tests := []struct {
		name       string
		stdin      string
		args       []string
		wantCode   int
		wantOut    string // substring of stdout
		wantErr    string // substring of stderr
		wantNoOut  bool   // stdout must be empty
		wantNoDiag bool   // stderr must be empty
	}{
		{name: "add", args: []string{"add", "write", "tests"}, wantCode: exitOK, wantOut: "added #1 write tests", wantNoDiag: true},
		{name: "add from pipe", stdin: "one\n\ntwo\n", args: []string{"add", "-q"}, wantCode: exitOK, wantOut: "2\n3\n", wantNoDiag: true},
		{name: "add with nothing", args: []string{"add"}, stdin: "  \n", wantCode: exitUsage, wantErr: "no title", wantNoOut: true},
		{name: "list pending", args: []string{"list"}, wantCode: exitOK, wantOut: "[ ] #1 write tests", wantNoDiag: true},
		{name: "done", args: []string{"done", "1"}, wantCode: exitOK, wantOut: "completed #1", wantNoDiag: true},
		{name: "done hides from list", args: []string{"list"}, wantCode: exitOK, wantOut: "#2 one", wantNoDiag: true},
		{name: "unknown id is a failure", args: []string{"done", "42"}, wantCode: exitFailure, wantErr: "no task with id 42", wantNoOut: true},
		{name: "non-numeric id is a usage error", args: []string{"rm", "abc"}, wantCode: exitUsage, wantErr: "not a task id", wantNoOut: true},
		{name: "unknown command", args: []string{"frob"}, wantCode: exitUsage, wantErr: "unknown command", wantNoOut: true},
		{name: "bad flag", args: []string{"list", "-zzz"}, wantCode: exitUsage, wantErr: "not defined", wantNoOut: true},
		{name: "help goes to stdout with exit 0", args: []string{"list", "-h"}, wantCode: exitOK, wantOut: "-json", wantNoDiag: true},
		{name: "rm", args: []string{"rm", "1", "2"}, wantCode: exitOK, wantOut: "removed #2", wantNoDiag: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := call(tc.stdin, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stdout=%q stderr=%q)", code, tc.wantCode, out, errOut)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("stdout = %q, want substring %q", out, tc.wantOut)
			}
			if !strings.Contains(errOut, tc.wantErr) {
				t.Errorf("stderr = %q, want substring %q", errOut, tc.wantErr)
			}
			if tc.wantNoOut && out != "" {
				t.Errorf("stdout should be empty, got %q", out)
			}
			if tc.wantNoDiag && errOut != "" {
				t.Errorf("stderr should be empty, got %q", errOut)
			}
		})
	}
}

func TestListJSONIsMachineReadable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.json")
	var out, errOut bytes.Buffer
	// An empty store must print [] (valid JSON), not null and not a human message on stdout.
	if code := runIO([]string{"-file", file, "list", "-json"}, strings.NewReader(""), &out, &errOut); code != exitOK {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	var tasks []Task
	if err := json.Unmarshal(out.Bytes(), &tasks); err != nil || tasks == nil {
		t.Fatalf("stdout %q is not a JSON array: %v", out.String(), err)
	}
}
