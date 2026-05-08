package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
)

// runCapture invokes run() with the given args, captures stdout/stderr,
// and returns (exitCode, stdout, stderr).
func runCapture(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunNoArgsIsUsageError(t *testing.T) {
	code, stdout, stderr := runCapture()
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if stdout != "" {
		t.Errorf("expected empty stdout on usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "USAGE") {
		t.Errorf("stderr should contain manual, got: %q", stderr)
	}
}

func TestRunVersion(t *testing.T) {
	for _, arg := range []string{"version", "-v", "--version"} {
		t.Run(arg, func(t *testing.T) {
			code, stdout, _ := runCapture(arg)
			if code != exitSuccess {
				t.Errorf("exit code = %d, want %d", code, exitSuccess)
			}
			want := "ralph " + version + "\n"
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			code, stdout, stderr := runCapture(arg)
			if code != exitSuccess {
				t.Errorf("exit code = %d, want %d", code, exitSuccess)
			}
			if !strings.Contains(stdout, "USAGE") || !strings.Contains(stdout, "REQUIREMENT IDS") {
				t.Errorf("help output missing expected sections: %q", stdout)
			}
			if stderr != "" {
				t.Errorf("expected empty stderr on help, got %q", stderr)
			}
		})
	}
}

func TestRunNewID(t *testing.T) {
	code, stdout, _ := runCapture("newid")
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	id := strings.TrimSuffix(stdout, "\n")
	if _, err := idgen.TimeOf(id); err != nil {
		t.Errorf("printed id %q is not canonical: %v", id, err)
	}
}

func TestRunNewIDRejectsExtraArgs(t *testing.T) {
	code, _, stderr := runCapture("newid", "extra")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "no arguments") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRunTimeOfRoundTrip(t *testing.T) {
	id := idgen.New()
	code, stdout, stderr := runCapture("time-of", id)
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}
	out := strings.TrimSuffix(stdout, "\n")
	// Format: 2006-01-02T15:04:05.000Z — ASCII sanity check.
	if len(out) != len("2006-01-02T15:04:05.000Z") || !strings.HasSuffix(out, "Z") {
		t.Errorf("time-of output not in expected format: %q", out)
	}
}

func TestRunTimeOfRequiresExactlyOneArg(t *testing.T) {
	cases := [][]string{
		{"time-of"},
		{"time-of", "a", "b"},
	}
	for _, args := range cases {
		code, _, stderr := runCapture(args...)
		if code != exitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(stderr, "exactly one ID") {
			t.Errorf("%v: stderr = %q", args, stderr)
		}
	}
}

func TestRunTimeOfInvalidID(t *testing.T) {
	code, _, stderr := runCapture("time-of", "not-an-id")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "ralph:") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRunInitCreatesSkeleton(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "newproj")

	code, _, stderr := runCapture("init", tmp)
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}

	reqsDir := filepath.Join(tmp, "reqs")
	entries, err := os.ReadDir(reqsDir)
	if err != nil {
		t.Fatalf("read reqs dir: %v", err)
	}
	got := make(map[string]bool, len(entries))
	for _, e := range entries {
		got[e.Name()] = true
	}
	for _, want := range []string{"OVERVIEW.md", "INTERACTIVE.md"} {
		if !got[want] {
			t.Errorf("missing %s in %s", want, reqsDir)
		}
	}
	if len(entries) != 2 {
		t.Errorf("reqs/ has %d entries, want 2: %v", len(entries), entries)
	}

	overview, err := os.ReadFile(filepath.Join(reqsDir, "OVERVIEW.md"))
	if err != nil {
		t.Fatalf("read OVERVIEW.md: %v", err)
	}
	overviewLower := strings.ToLower(string(overview))
	for _, banned := range []string{"ralph", "orchestrator"} {
		if strings.Contains(overviewLower, banned) {
			t.Errorf("OVERVIEW.md must stay generic; found %q", banned)
		}
	}

	interactive, err := os.ReadFile(filepath.Join(reqsDir, "INTERACTIVE.md"))
	if err != nil {
		t.Fatalf("read INTERACTIVE.md: %v", err)
	}
	body := string(interactive)
	for _, want := range []string{"R-XXXX-XXXX", "ralph newid", "ralph time-of"} {
		if !strings.Contains(body, want) {
			t.Errorf("INTERACTIVE.md missing %q", want)
		}
	}
	if !strings.Contains(body, "never creates, modifies") {
		t.Error("INTERACTIVE.md missing the orchestrator-never-edits statement")
	}
	// WHAT/WHY-not-HOW heading should be prominent — within the first
	// quarter of the file.
	idx := strings.Index(body, "WHAT and WHY, never HOW")
	if idx < 0 {
		t.Error("INTERACTIVE.md missing WHAT/WHY-not-HOW heading")
	} else if idx > len(body)/4 {
		t.Errorf("WHAT/WHY-not-HOW heading at offset %d; expected within first quarter (%d)", idx, len(body)/4)
	}
}

func TestRunInitRefusesExistingReqs(t *testing.T) {
	tmp := t.TempDir()
	reqsDir := filepath.Join(tmp, "reqs")
	if err := os.MkdirAll(reqsDir, 0o755); err != nil {
		t.Fatalf("seed reqs dir: %v", err)
	}
	sentinel := filepath.Join(reqsDir, "preexisting.md")
	if err := os.WriteFile(sentinel, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	code, _, stderr := runCapture("init", tmp)
	if code != exitRuntime {
		t.Errorf("exit = %d, want %d (stderr=%q)", code, exitRuntime, stderr)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr = %q, want a clear refusal message", stderr)
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel disappeared: %v", err)
	}
	if string(got) != "keep me\n" {
		t.Errorf("sentinel modified: %q", got)
	}
	entries, err := os.ReadDir(reqsDir)
	if err != nil {
		t.Fatalf("read reqs dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("reqs/ now has %d entries, expected the original 1", len(entries))
	}
}

func TestRunInitRequiresExactlyOneArg(t *testing.T) {
	cases := [][]string{
		{"init"},
		{"init", "a", "b"},
	}
	for _, args := range cases {
		code, _, stderr := runCapture(args...)
		if code != exitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(stderr, "PATH") {
			t.Errorf("%v: stderr = %q", args, stderr)
		}
	}
}

func TestRunHelpListsInit(t *testing.T) {
	_, stdout, _ := runCapture("help")
	if !strings.Contains(stdout, "ralph init PATH") {
		t.Errorf("help output missing init subcommand: %q", stdout)
	}
}

func TestRunLoopRequiresWorkdir(t *testing.T) {
	// All flags consumed, no positional argument.
	code, _, stderr := runCapture("--model=sonnet")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "WORKDIR") {
		t.Errorf("stderr should mention WORKDIR, got %q", stderr)
	}
}

func TestRunLoopRejectsUnknownFlag(t *testing.T) {
	code, _, _ := runCapture("--definitely-not-a-flag", ".")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
}

func TestRunLoopRejectsExtraPositional(t *testing.T) {
	code, _, stderr := runCapture("workdir", "extra")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "WORKDIR") {
		t.Errorf("stderr = %q", stderr)
	}
}
