package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// resetSeed scaffolds a project tree under t.TempDir() (using
// scaffoldProject so the seed exactly matches `ralph init` output),
// then sprinkles agent-generated detritus inside app-root/ so each
// test can assert that it was wiped: source files, a nested directory,
// the .ralph/ state directory, and a nested .git so we prove `git`
// trees are not special-cased. Returns the project root.
func resetSeed(t *testing.T, reqsName, appRootName, helperName string) string {
	t.Helper()
	root := t.TempDir()
	if err := scaffoldProject(root, reqsName, appRootName, helperName); err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}

	app := filepath.Join(root, appRootName)
	seed := []struct {
		path string
		body string
	}{
		{filepath.Join(app, "main.go"), "package main\nfunc main() {}\n"},
		{filepath.Join(app, "internal", "thing", "thing.go"), "package thing\n"},
		{filepath.Join(app, ".ralph", "handoff.md"), "next: do the thing\n"},
		{filepath.Join(app, ".ralph", "requirements-verified.jsonl"), `{"id":"R-XXXX-XXXX"}` + "\n"},
		{filepath.Join(app, ".git", "HEAD"), "ref: refs/heads/main\n"},
	}
	for _, s := range seed {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(s.path), err)
		}
		if err := os.WriteFile(s.path, []byte(s.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", s.path, err)
		}
	}
	return root
}

// chdirTo cd's into dir for the duration of the test, restoring the
// previous cwd via t.Cleanup. Several reset tests need this because
// the subcommand's positional defaults to ".".
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// listAppRoot returns the sorted names of every entry inside the
// app-root directory under root.
func listAppRoot(t *testing.T, root, appRootName string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, appRootName))
	if err != nil {
		t.Fatalf("read app-root: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestRun_Reset_WipesAndRewritesAgents(t *testing.T) {
	root := resetSeed(t, "reqs", "app-root", "helper")
	chdirTo(t, root)

	code, _, stderr := runCapture("reset")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}

	// Only AGENTS.md should remain inside app-root/.
	got := listAppRoot(t, root, "app-root")
	want := []string{"AGENTS.md"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("app-root contents = %v, want %v", got, want)
	}

	// AGENTS.md is the rendered template — check for the path token
	// the template substitutes and confirm no template literals leaked.
	body, err := os.ReadFile(filepath.Join(root, "app-root", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	s := string(body)
	if strings.Contains(s, "{{") {
		t.Errorf("AGENTS.md still has unreplaced template tokens: %q", s)
	}
	if !strings.Contains(s, "../reqs/") {
		t.Errorf("AGENTS.md should reference ../reqs/; got: %q", s)
	}

	// Sibling subdirectories must be untouched.
	for _, p := range []string{
		filepath.Join(root, "reqs", "OVERVIEW.md"),
		filepath.Join(root, "helper", "AGENTS.md"),
	} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("sibling %s should still exist: %v", p, err)
		}
	}
}

func TestRun_Reset_CustomFlagsRenderMatchingAgents(t *testing.T) {
	root := resetSeed(t, "spec", "build", "designer")
	chdirTo(t, root)

	code, _, stderr := runCapture("reset", "--reqs=spec", "--app-root=build", "--helper=designer")
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}

	got := listAppRoot(t, root, "build")
	if len(got) != 1 || got[0] != "AGENTS.md" {
		t.Errorf("build contents = %v, want [AGENTS.md]", got)
	}

	body, err := os.ReadFile(filepath.Join(root, "build", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "../spec/") {
		t.Errorf("AGENTS.md should reference ../spec/; got: %q", s)
	}
	if strings.Contains(s, "../reqs/") {
		t.Errorf("AGENTS.md should not reference default ../reqs/: %q", s)
	}
}

func TestRun_Reset_AcceptsPositional(t *testing.T) {
	root := resetSeed(t, "reqs", "app-root", "helper")

	code, _, stderr := runCapture("reset", root)
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}
	// Restore cwd because runCapture chdir'd into root.
	t.Cleanup(func() { _ = os.Chdir("/") })

	got := listAppRoot(t, root, "app-root")
	if len(got) != 1 || got[0] != "AGENTS.md" {
		t.Errorf("app-root contents = %v, want [AGENTS.md]", got)
	}
}

func TestRun_Reset_RefusesNonProjectRoot(t *testing.T) {
	// cwd has no helper/, reqs/, or app-root/.
	tmp := t.TempDir()
	chdirTo(t, tmp)

	code, _, stderr := runCapture("reset")
	if code != exitRuntime {
		t.Errorf("exit = %d, want %d (stderr=%q)", code, exitRuntime, stderr)
	}
	if !strings.Contains(stderr, "not a ralph project root") {
		t.Errorf("stderr should mention 'not a ralph project root', got %q", stderr)
	}
}

// TestRun_Reset_RefusesPartialProject seeds only some of the three
// expected subdirectories and asserts that nothing in the existing
// trees is modified — proving the project-shape check fires before
// the wipe.
func TestRun_Reset_RefusesPartialProject(t *testing.T) {
	cases := []struct {
		name    string
		present []string
	}{
		{"only-app-root", []string{"app-root"}},
		{"missing-helper", []string{"reqs", "app-root"}},
		{"missing-reqs", []string{"app-root", "helper"}},
		{"missing-app-root", []string{"reqs", "helper"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmp := t.TempDir()
			for _, sub := range c.present {
				if err := os.MkdirAll(filepath.Join(tmp, sub), 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", sub, err)
				}
			}
			// Sentinel inside app-root if it was seeded — we expect it
			// to survive the refusal.
			sentinel := ""
			for _, sub := range c.present {
				if sub == "app-root" {
					sentinel = filepath.Join(tmp, "app-root", "keep.txt")
					if err := os.WriteFile(sentinel, []byte("hi"), 0o644); err != nil {
						t.Fatalf("seed sentinel: %v", err)
					}
				}
			}

			code, _, stderr := runCapture("reset", tmp)
			t.Cleanup(func() { _ = os.Chdir("/") })
			if code != exitRuntime {
				t.Errorf("exit = %d, want %d (stderr=%q)", code, exitRuntime, stderr)
			}
			if !strings.Contains(stderr, "not a ralph project root") {
				t.Errorf("stderr should mention 'not a ralph project root', got %q", stderr)
			}
			if sentinel != "" {
				if _, err := os.Lstat(sentinel); err != nil {
					t.Errorf("sentinel destroyed by refused reset: %v", err)
				}
			}
		})
	}
}

func TestRun_Reset_BadPositional_ChdirFails(t *testing.T) {
	code, _, stderr := runCapture("reset", "/no/such/directory/abcdef")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d (stderr=%q)", code, exitUsage, stderr)
	}
	if !strings.Contains(stderr, "chdir") {
		t.Errorf("stderr should mention chdir, got %q", stderr)
	}
}

func TestRun_Reset_RejectsExtraPositional(t *testing.T) {
	code, _, stderr := runCapture("reset", "a", "b")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d (stderr=%q)", code, exitUsage, stderr)
	}
	if !strings.Contains(stderr, "at most one positional") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRun_Reset_RejectsBadFlags(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty-reqs", []string{"reset", "--reqs=", tmp}, "must not be empty"},
		{"slash-in-app-root", []string{"reset", "--app-root=foo/bar", tmp}, "plain directory names"},
		{"equal-names", []string{"reset", "--reqs=x", "--app-root=x", tmp}, "must all differ"},
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

func TestRun_Help_ListsReset(t *testing.T) {
	_, stdout, _ := runCapture("help")
	if !strings.Contains(stdout, "ralph reset") {
		t.Errorf("help output missing reset subcommand: %q", stdout)
	}
}

// TestRun_Reset_OutputsNothingOnSuccess pins the silence contract:
// successful reset is fire-and-forget. Operators chain it with `&&`,
// and a chatty CLI would clutter that.
func TestRun_Reset_OutputsNothingOnSuccess(t *testing.T) {
	root := resetSeed(t, "reqs", "app-root", "helper")
	chdirTo(t, root)

	var stdout, stderr bytes.Buffer
	code := run([]string{"reset"}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("expected empty stdout, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("expected empty stderr, got %q", stderr.String())
	}
}
