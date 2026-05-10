package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
)

// runCapture invokes run() with the given args, captures stdout/stderr,
// and returns (exitCode, stdout, stderr).
func runCapture(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRun_NoArgs_IsUsageError(t *testing.T) {
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

func TestRun_Version(t *testing.T) {
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

func TestRun_Help(t *testing.T) {
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

func TestRun_NewID(t *testing.T) {
	code, stdout, _ := runCapture("newid")
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	id := strings.TrimSuffix(stdout, "\n")
	if _, err := idgen.TimeOf(id); err != nil {
		t.Errorf("printed id %q is not canonical: %v", id, err)
	}
}

func TestRun_NewID_RejectsExtraArgs(t *testing.T) {
	code, _, stderr := runCapture("newid", "extra")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "no positional arguments") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRun_NewID_BatchProducesDistinctMonotonicIDs(t *testing.T) {
	const n = 5
	code, stdout, stderr := runCapture("newid", "--number", "5")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d: %q", len(lines), n, stdout)
	}
	seen := make(map[string]bool, n)
	var prev time.Time
	for i, id := range lines {
		if seen[id] {
			t.Errorf("duplicate ID at line %d: %q", i, id)
		}
		seen[id] = true
		ts, err := idgen.TimeOf(id)
		if err != nil {
			t.Fatalf("line %d %q: %v", i, id, err)
		}
		if i > 0 && !ts.After(prev) {
			t.Errorf("line %d ts %v not strictly after prev %v", i, ts, prev)
		}
		prev = ts
	}
}

func TestRun_NewID_RejectsNonPositiveNumber(t *testing.T) {
	for _, n := range []string{"0", "-1"} {
		t.Run("number="+n, func(t *testing.T) {
			code, _, stderr := runCapture("newid", "--number", n)
			if code != exitUsage {
				t.Errorf("exit = %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr, "--number must be > 0") {
				t.Errorf("stderr = %q", stderr)
			}
		})
	}
}

func TestRun_TimeOf_RoundTrip(t *testing.T) {
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

func TestRun_TimeOf_RequiresExactlyOneArg(t *testing.T) {
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

func TestRun_TimeOf_InvalidID(t *testing.T) {
	code, _, stderr := runCapture("time-of", "not-an-id")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "ralph:") {
		t.Errorf("stderr = %q", stderr)
	}
}

// decodeUnverified parses the single-line JSON [unverifiedReport]
// printed by `ralph unverified`. Used by every test in this group so
// the JSON shape is asserted in one place.
func decodeUnverified(t *testing.T, stdout string) unverifiedReport {
	t.Helper()
	var rep unverifiedReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rep); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return rep
}

// chdirTemp builds a workdir layout under t.TempDir(), seeds its
// reqs/ directory and .ralph/ ledger from the supplied bodies, chdirs
// the test process into the workdir, and registers a t.Cleanup that
// restores the original cwd. `ralph unverified` reads the workdir
// from the current directory, so every CLI test in this group needs
// the same setup.
func chdirTemp(t *testing.T, specBody, ledgerBody string) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "reqs"), 0o755); err != nil {
		t.Fatalf("mkdir reqs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "reqs", "spec.md"), []byte(specBody), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	if ledgerBody != "" {
		ralphDir := filepath.Join(tmp, ".ralph")
		if err := os.MkdirAll(ralphDir, 0o755); err != nil {
			t.Fatalf("mkdir .ralph: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ralphDir, "requirements-verified.jsonl"), []byte(ledgerBody), 0o644); err != nil {
			t.Fatalf("write ledger: %v", err)
		}
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return tmp
}

func TestRun_Unverified_PendingShape(t *testing.T) {
	chdirTemp(t,
		"R-052Y-EKE0\nR-3HX7-91ZA\nR-9PQR-12ST\nR-XXXX-XXXX (placeholder)\n",
		`{"id":"R-052Y-EKE0"}`+"\n")

	code, stdout, stderr := runCapture("unverified")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}
	rep := decodeUnverified(t, stdout)
	if rep.Status != "pending" {
		t.Errorf("Status = %q, want %q", rep.Status, "pending")
	}
	if rep.Count != 2 {
		t.Errorf("Count = %d, want 2", rep.Count)
	}
	want := []string{"R-3HX7-91ZA", "R-9PQR-12ST"}
	if !reflect.DeepEqual(rep.List, want) {
		t.Errorf("List = %v, want %v", rep.List, want)
	}
}

func TestRun_Unverified_DoneShape(t *testing.T) {
	chdirTemp(t,
		"R-052Y-EKE0\n",
		`{"id":"R-052Y-EKE0"}`+"\n")

	code, stdout, stderr := runCapture("unverified")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}
	rep := decodeUnverified(t, stdout)
	if rep.Status != "done" {
		t.Errorf("Status = %q, want %q", rep.Status, "done")
	}
	if rep.Count != 0 {
		t.Errorf("Count = %d, want 0", rep.Count)
	}
	// Empty list must serialise as `[]`, never `null`, so JSON consumers
	// can index without a nil-check.
	if rep.List == nil {
		t.Error("List should be non-nil empty slice, got nil")
	}
	if len(rep.List) != 0 {
		t.Errorf("List = %v, want empty", rep.List)
	}
	if !strings.Contains(stdout, `"list":[]`) {
		t.Errorf("done payload should encode list as []: %q", stdout)
	}
}

func TestRun_Unverified_RejectsPositional(t *testing.T) {
	chdirTemp(t, "R-052Y-EKE0\n", "")
	code, _, stderr := runCapture("unverified", "some-path")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "no positional") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRun_Init_CreatesSkeleton(t *testing.T) {
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

func TestRun_Init_RefusesExistingReqs(t *testing.T) {
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

func TestRun_Init_RequiresExactlyOneArg(t *testing.T) {
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

func TestRun_Help_ListsInit(t *testing.T) {
	_, stdout, _ := runCapture("help")
	if !strings.Contains(stdout, "ralph init PATH") {
		t.Errorf("help output missing init subcommand: %q", stdout)
	}
}

func TestRun_Loop_RequiresWorkdir(t *testing.T) {
	// All flags consumed, no positional argument.
	code, _, stderr := runCapture("--model=sonnet")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "WORKDIR") {
		t.Errorf("stderr should mention WORKDIR, got %q", stderr)
	}
}

func TestRun_Loop_RejectsUnknownFlag(t *testing.T) {
	code, _, _ := runCapture("--definitely-not-a-flag", ".")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
}

func TestRun_Loop_RejectsExtraPositional(t *testing.T) {
	code, _, stderr := runCapture("workdir", "extra")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "WORKDIR") {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestRunVersionAfterOtherFlags pins the bug fix where
// `ralph --reqs=foo --version` was previously misrouted into the loop
// driver because the dispatcher only inspected args[0]. After moving
// --version onto the loop's FlagSet, the flag parser sees it
// regardless of position.
func TestRun_Version_AfterOtherFlags(t *testing.T) {
	cases := [][]string{
		{"--reqs=foo", "--version"},
		{"--reqs=foo", "-v"},
		{"--model=sonnet", "--effort=high", "--version"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, stderr := runCapture(args...)
			if code != exitSuccess {
				t.Errorf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
			}
			want := "ralph " + version + "\n"
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
		})
	}
}

// TestRunHelpAfterOtherFlags is the --help twin of the --version test.
func TestRun_Help_AfterOtherFlags(t *testing.T) {
	cases := [][]string{
		{"--reqs=foo", "--help"},
		{"--reqs=foo", "-h"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, _ := runCapture(args...)
			if code != exitSuccess {
				t.Errorf("exit = %d, want %d", code, exitSuccess)
			}
			if !strings.Contains(stdout, "USAGE") {
				t.Errorf("help output missing USAGE section: %q", stdout)
			}
		})
	}
}

func TestRun_Loop_RejectsEmptyEngine(t *testing.T) {
	code, _, stderr := runCapture("--engine=", ".")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "--engine") {
		t.Errorf("stderr should mention --engine, got %q", stderr)
	}
}

func TestRun_Help_DocumentsEngine(t *testing.T) {
	_, stdout, _ := runCapture("help")
	if !strings.Contains(stdout, "--engine=COMMAND") {
		t.Errorf("help output missing --engine flag row: %q", stdout)
	}
}

