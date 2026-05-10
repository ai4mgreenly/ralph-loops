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

// chdirTemp builds a project layout under t.TempDir() and chdirs into
// the app-root subdirectory. Layout matches what `ralph init` produces
// (minus the AGENTS.md files, which would trip the foot-gun guard):
//
//	<tmp>/reqs/spec.md                                  ← the spec
//	<tmp>/app-root/.ralph/requirements-verified.jsonl   ← the ledger
//
// The process cwd is set to <tmp>/app-root and restored via t.Cleanup.
// `ralph unverified` defaults --reqs to ../reqs, so it resolves the
// spec correctly from inside app-root.
func chdirTemp(t *testing.T, specBody, ledgerBody string) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "reqs"), 0o755); err != nil {
		t.Fatalf("mkdir reqs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "reqs", "spec.md"), []byte(specBody), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	appRoot := filepath.Join(tmp, "app-root")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatalf("mkdir app-root: %v", err)
	}
	if ledgerBody != "" {
		ralphDir := filepath.Join(appRoot, ".ralph")
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
	if err := os.Chdir(appRoot); err != nil {
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

	// Verify the full scaffolded tree exists.
	for _, p := range []string{
		filepath.Join(tmp, "helper", "AGENTS.md"),
		filepath.Join(tmp, "reqs", "OVERVIEW.md"),
		filepath.Join(tmp, "app-root", "AGENTS.md"),
	} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("missing scaffolded path %s: %v", p, err)
		}
	}

	// No AGENTS.md or CLAUDE.md at the project root — and no
	// CLAUDE.md anywhere in the scaffold. See scaffoldProject's doc
	// comment: a root-level spec-helper would leak into the build
	// agent's context via claude's walk-up.
	for _, banned := range []string{
		filepath.Join(tmp, "AGENTS.md"),
		filepath.Join(tmp, "CLAUDE.md"),
		filepath.Join(tmp, "app-root", "CLAUDE.md"),
		filepath.Join(tmp, "helper", "CLAUDE.md"),
	} {
		if _, err := os.Lstat(banned); err == nil {
			t.Errorf("%s should not be scaffolded", banned)
		}
	}

	// OVERVIEW.md stays generic — no mention of ralph or orchestrator.
	overview, err := os.ReadFile(filepath.Join(tmp, "reqs", "OVERVIEW.md"))
	if err != nil {
		t.Fatalf("read OVERVIEW.md: %v", err)
	}
	overviewLower := strings.ToLower(string(overview))
	for _, banned := range []string{"ralph", "orchestrator"} {
		if strings.Contains(overviewLower, banned) {
			t.Errorf("OVERVIEW.md must stay generic; found %q", banned)
		}
	}

	// The helper AGENTS.md is the spec-helper persona; it carries
	// the WHAT/WHY-not-HOW heading, the requirement-ID conventions,
	// and the orchestrator-never-edits statement.
	helperAgents, err := os.ReadFile(filepath.Join(tmp, "helper", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read helper AGENTS.md: %v", err)
	}
	body := string(helperAgents)
	for _, want := range []string{"R-XXXX-XXXX", "ralph newid", "ralph time-of"} {
		if !strings.Contains(body, want) {
			t.Errorf("helper AGENTS.md missing %q", want)
		}
	}
	if !strings.Contains(body, "never creates, modifies") {
		t.Error("helper AGENTS.md missing the orchestrator-never-edits statement")
	}
	if !strings.Contains(body, "WHAT and WHY, never HOW") {
		t.Error("helper AGENTS.md missing WHAT/WHY-not-HOW heading")
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
		if !strings.Contains(stderr, "requires exactly one PATH") {
			t.Errorf("%v: stderr = %q", args, stderr)
		}
	}
}

func TestRun_Init_RejectsBadFlags(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty-reqs", []string{"init", "--reqs=", tmp}, "must not be empty"},
		{"slash-in-reqs", []string{"init", "--reqs=foo/bar", tmp}, "plain directory names"},
		{"equal-names", []string{"init", "--reqs=x", "--app-root=x", tmp}, "must all differ"},
		{"helper-equals-reqs", []string{"init", "--reqs=x", "--helper=x", tmp}, "must all differ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, stderr := runCapture(c.args...)
			if code != exitUsage {
				t.Errorf("exit = %d, want %d (stderr=%q)", code, exitUsage, stderr)
			}
			if !strings.Contains(stderr, c.want) {
				t.Errorf("stderr = %q, want substring %q", stderr, c.want)
			}
		})
	}
}

func TestRun_Init_AcceptsCustomFlags(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "newproj")
	code, _, stderr := runCapture("init", "--reqs=spec", "--app-root=build", tmp)
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}
	for _, p := range []string{
		filepath.Join(tmp, "spec", "OVERVIEW.md"),
		filepath.Join(tmp, "build", "AGENTS.md"),
	} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	appAgents, err := os.ReadFile(filepath.Join(tmp, "build", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read build AGENTS.md: %v", err)
	}
	if !strings.Contains(string(appAgents), "../spec/") {
		t.Errorf("build/AGENTS.md should reference ../spec/; got: %q", appAgents)
	}
}

func TestRun_Help_ListsInit(t *testing.T) {
	_, stdout, _ := runCapture("help")
	if !strings.Contains(stdout, "ralph init") {
		t.Errorf("help output missing init subcommand: %q", stdout)
	}
}

func TestRun_Loop_RejectsUnknownFlag(t *testing.T) {
	code, _, _ := runCapture("--definitely-not-a-flag")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
}

func TestRun_Loop_RejectsExtraPositional(t *testing.T) {
	// One positional (PROJECT_ROOT) is allowed; two is not. The
	// fs.NArg() > 1 check fires before any chdir, so we don't need
	// the cwd to look like a project root.
	code, _, stderr := runCapture("a", "b")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "at most one positional") {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestRun_Loop_RejectsMissingAppRoot trips the inverted foot-gun guard:
// cwd has no app-root/AGENTS.md, so the driver should refuse with
// exitUsage and a guidance message pointing at `ralph init`.
func TestRun_Loop_RejectsMissingAppRoot(t *testing.T) {
	tmp := t.TempDir()

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	code, _, stderr := runCapture("--model=sonnet")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d (stderr=%q)", code, exitUsage, stderr)
	}
	if !strings.Contains(stderr, "no app-root/AGENTS.md found") {
		t.Errorf("stderr should mention 'no app-root/AGENTS.md found', got %q", stderr)
	}
}

// TestRun_Loop_AcceptsPositional verifies the new PROJECT_ROOT
// positional: ralph os.Chdir's into the supplied directory before the
// foot-gun guard runs. When app-root/AGENTS.md exists in that
// directory, the guard passes and execution proceeds to loop.Run,
// which then fails because the engine command can't be resolved on
// PATH. The point is to prove the chdir + guard plumbing works; we
// don't care exactly which runtime error loop.Run raises.
func TestRun_Loop_AcceptsPositional(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app-root")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatalf("mkdir app-root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "AGENTS.md"), []byte("app\n"), 0o644); err != nil {
		t.Fatalf("write app AGENTS.md: %v", err)
	}

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	code, _, stderr := runCapture("--engine=this-engine-does-not-exist-1234567", tmp)
	if code == exitUsage {
		t.Errorf("exit = %d, want non-usage (stderr=%q)", code, stderr)
	}
	if strings.Contains(stderr, "no app-root/AGENTS.md found") {
		t.Errorf("foot-gun should not fire when marker exists, got %q", stderr)
	}
}

// TestRun_Loop_BadPositional_ChdirFails covers the chdir failure path:
// a non-existent PROJECT_ROOT should be reported as a usage error with
// a "chdir" diagnostic.
func TestRun_Loop_BadPositional_ChdirFails(t *testing.T) {
	code, _, stderr := runCapture("--engine=foo", "/no/such/directory/abcdef")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d (stderr=%q)", code, exitUsage, stderr)
	}
	if !strings.Contains(stderr, "chdir") {
		t.Errorf("stderr should mention chdir, got %q", stderr)
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
	code, _, stderr := runCapture("--engine=")
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
