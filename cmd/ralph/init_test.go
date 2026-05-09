package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldReqs_CreatesReqsAndSkeletonFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := scaffoldReqs(dir); err != nil {
		t.Fatalf("scaffoldReqs: %v", err)
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

func TestScaffoldReqs_RefusesIfReqsExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "reqs"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := scaffoldReqs(dir)
	if err == nil {
		t.Fatal("scaffoldReqs should refuse when reqs/ exists; got nil error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention already exists, got %v", err)
	}
}

func TestScaffoldReqs_CreatesParentPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "project")
	if err := scaffoldReqs(target); err != nil {
		t.Fatalf("scaffoldReqs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "reqs", "OVERVIEW.md")); err != nil {
		t.Errorf("expected OVERVIEW.md created under nested path: %v", err)
	}
}

func TestScaffoldReqs_StatErrorReturnsWrappedError(t *testing.T) {
	t.Parallel()
	// Use a path under a non-directory parent. Stat on the child
	// returns an error other than fs.ErrNotExist.
	dir := t.TempDir()
	parent := filepath.Join(dir, "file")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := scaffoldReqs(parent)
	if err == nil {
		t.Fatal("expected scaffoldReqs to fail when path is a file")
	}
}
