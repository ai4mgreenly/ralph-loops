package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_CreatesReqsAndSkeletonFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, name := range []string{"OVERVIEW.md", "INTERACTIVE.md"} {
		p := filepath.Join(dir, "reqs", name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if len(b) == 0 {
			t.Errorf("%s: empty content", name)
		}
	}
}

func TestInit_RefusesIfReqsExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "reqs"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := Init(dir)
	if err == nil {
		t.Fatal("Init should refuse when reqs/ exists; got nil error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention already exists, got %v", err)
	}
}

func TestInit_CreatesParentPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "project")
	if err := Init(target); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "reqs", "OVERVIEW.md")); err != nil {
		t.Errorf("expected OVERVIEW.md created under nested path: %v", err)
	}
}

func TestInit_StatErrorReturnsWrappedError(t *testing.T) {
	t.Parallel()
	// Use a path under a non-directory parent. Stat on the child
	// returns an error other than fs.ErrNotExist.
	dir := t.TempDir()
	parent := filepath.Join(dir, "file")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := Init(parent)
	if err == nil {
		t.Fatal("expected Init to fail when path is a file")
	}
}
