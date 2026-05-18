package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scaffoldOK is the bare minimum sanity check used by several tests
// below: the named relative path exists under root and (when expected
// to be a file) is non-empty.
func mustExist(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	// Only enforce non-emptiness for regular files; symlinks have
	// target-length sizes that don't matter to us.
	if info.Mode().IsRegular() {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(b) == 0 {
			t.Errorf("%s: empty content", path)
		}
	}
}

func TestScaffoldProject_CreatesFullTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := scaffoldProject(dir, "reqs", "app-root", "helper"); err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}

	mustExist(t, filepath.Join(dir, "helper", "AGENTS.md"))
	mustExist(t, filepath.Join(dir, "reqs", "OVERVIEW.md"))
	mustExist(t, filepath.Join(dir, "app-root", "AGENTS.md"))

	// No AGENTS.md or CLAUDE.md should exist at the project root, and
	// none inside the subdirectories beyond the two scaffolded
	// AGENTS.md files — see scaffoldProject's doc comment. (pi does no
	// walk-up; the split is plain role separation.)
	for _, banned := range []string{
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(dir, "CLAUDE.md"),
		filepath.Join(dir, "app-root", "CLAUDE.md"),
		filepath.Join(dir, "helper", "CLAUDE.md"),
	} {
		if _, err := os.Lstat(banned); err == nil {
			t.Errorf("%s should not be scaffolded", banned)
		}
	}
}

func TestScaffoldProject_DefaultTemplatingHasLiterals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := scaffoldProject(dir, "reqs", "app-root", "helper"); err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}

	for _, p := range []string{
		filepath.Join(dir, "helper", "AGENTS.md"),
		filepath.Join(dir, "app-root", "AGENTS.md"),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		s := string(b)
		if strings.Contains(s, "{{") {
			t.Errorf("%s still has unreplaced template tokens: %q", p, s)
		}
	}

	appAgents, err := os.ReadFile(filepath.Join(dir, "app-root", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read app AGENTS.md: %v", err)
	}
	if !bytes.Contains(appAgents, []byte("../reqs/")) {
		t.Errorf("app-root AGENTS.md should reference ../reqs/; got: %q", appAgents)
	}

	helperAgents, err := os.ReadFile(filepath.Join(dir, "helper", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read helper AGENTS.md: %v", err)
	}
	if !bytes.Contains(helperAgents, []byte("../reqs/")) {
		t.Errorf("helper AGENTS.md should reference ../reqs/; got: %q", helperAgents)
	}
}

func TestScaffoldProject_CustomNamesGetSubstituted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := scaffoldProject(dir, "spec", "build", "designer"); err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}

	mustExist(t, filepath.Join(dir, "spec", "OVERVIEW.md"))
	mustExist(t, filepath.Join(dir, "build", "AGENTS.md"))
	mustExist(t, filepath.Join(dir, "designer", "AGENTS.md"))
	if _, err := os.Lstat(filepath.Join(dir, "build", "CLAUDE.md")); err == nil {
		t.Errorf("build/CLAUDE.md should not be scaffolded")
	}

	appAgents, err := os.ReadFile(filepath.Join(dir, "build", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read app AGENTS.md: %v", err)
	}
	body := string(appAgents)
	if strings.Contains(body, "{{") {
		t.Errorf("unreplaced template tokens in build/AGENTS.md: %q", body)
	}
	if !strings.Contains(body, "../spec/") {
		t.Errorf("build/AGENTS.md should reference ../spec/; got: %q", body)
	}
	// And it should NOT mention the default name "reqs" via its
	// templated path form.
	if strings.Contains(body, "../reqs/") {
		t.Errorf("build/AGENTS.md still references default ../reqs/: %q", body)
	}

	helperAgents, err := os.ReadFile(filepath.Join(dir, "designer", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read designer/AGENTS.md: %v", err)
	}
	hbody := string(helperAgents)
	if strings.Contains(hbody, "{{") {
		t.Errorf("unreplaced template tokens in designer/AGENTS.md: %q", hbody)
	}
	if !strings.Contains(hbody, "../spec/") {
		t.Errorf("designer/AGENTS.md should reference ../spec/; got: %q", hbody)
	}
	if !strings.Contains(hbody, "../build/") {
		t.Errorf("designer/AGENTS.md should reference ../build/; got: %q", hbody)
	}
	if strings.Contains(hbody, "../reqs/") {
		t.Errorf("designer/AGENTS.md still references default ../reqs/: %q", hbody)
	}
	if strings.Contains(hbody, "../app-root/") {
		t.Errorf("designer/AGENTS.md still references default ../app-root/: %q", hbody)
	}
}

// existCases is the table of pre-existing-path cases. For each entry
// we seed exactly one file (or directory) inside an otherwise-empty
// root and verify scaffoldProject refuses.
func TestScaffoldProject_RefusesWhenAnyTargetExists(t *testing.T) {
	t.Parallel()
	// Each case names the pre-existing path (relative to root) and
	// whether it's a directory.
	cases := []struct {
		name string
		path string
		dir  bool
	}{
		{"reqs-dir-exists", "reqs", true},
		{"app-root-dir-exists", "app-root", true},
		{"helper-dir-exists", "helper", true},
		{"helper-agents-exists", filepath.Join("helper", "AGENTS.md"), false},
		{"app-agents-exists", filepath.Join("app-root", "AGENTS.md"), false},
		{"overview-exists", filepath.Join("reqs", "OVERVIEW.md"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, c.path)
			if c.dir {
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatalf("seed dir %s: %v", target, err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatalf("mkdir parent of %s: %v", target, err)
				}
				if err := os.WriteFile(target, []byte("sentinel\n"), 0o644); err != nil {
					t.Fatalf("seed file %s: %v", target, err)
				}
			}

			err := scaffoldProject(dir, "reqs", "app-root", "helper")
			if err == nil {
				t.Fatalf("scaffoldProject should refuse when %s exists; got nil error", c.path)
			}
			if !strings.Contains(err.Error(), "already exists") {
				t.Errorf("error should mention already exists, got %v", err)
			}

			// Sentinel survives untouched when it's a file.
			if !c.dir {
				b, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("sentinel disappeared: %v", err)
				}
				if string(b) != "sentinel\n" {
					t.Errorf("sentinel modified: %q", b)
				}
			}
		})
	}
}

func TestScaffoldProject_CreatesParentPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deeper", "project")
	if err := scaffoldProject(target, "reqs", "app-root", "helper"); err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}
	mustExist(t, filepath.Join(target, "reqs", "OVERVIEW.md"))
	mustExist(t, filepath.Join(target, "app-root", "AGENTS.md"))
	mustExist(t, filepath.Join(target, "helper", "AGENTS.md"))
}

func TestScaffoldProject_RefusesNonDirectoryParent(t *testing.T) {
	t.Parallel()
	// The root path is itself a regular file. The first Lstat call
	// inside scaffoldProject hits a target path under that root with a
	// non-not-exist error, surfacing a clean failure.
	dir := t.TempDir()
	root := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(root, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := scaffoldProject(root, "reqs", "app-root", "helper")
	if err == nil {
		t.Fatal("expected scaffoldProject to fail when root is a regular file")
	}
}

// TestRunInit_CustomFlags exercises the flag parser inside runInit
// (not just scaffoldProject) to confirm --reqs, --app-root, and
// --helper flow through to the produced tree.
func TestRunInit_CustomFlags(t *testing.T) {
	t.Parallel()
	tmp := filepath.Join(t.TempDir(), "proj")
	var stdout, stderr bytes.Buffer
	code := runInit([]string{"--reqs=spec", "--app-root=build", "--helper=designer", tmp}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitSuccess, stderr.String())
	}
	mustExist(t, filepath.Join(tmp, "spec", "OVERVIEW.md"))
	mustExist(t, filepath.Join(tmp, "build", "AGENTS.md"))
	mustExist(t, filepath.Join(tmp, "designer", "AGENTS.md"))

	body, err := os.ReadFile(filepath.Join(tmp, "build", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read build AGENTS.md: %v", err)
	}
	if !strings.Contains(string(body), "../spec/") {
		t.Errorf("build/AGENTS.md should reference ../spec/; got: %q", body)
	}
}
