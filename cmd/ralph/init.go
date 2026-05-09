package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed skel/OVERVIEW.md
var skelOverview string

//go:embed skel/INTERACTIVE.md
var skelInteractive string

// scaffoldReqs creates path/reqs/ and writes the OVERVIEW.md and
// INTERACTIVE.md templates into it. path is created if missing; if
// path/reqs/ already exists, the call refuses without modifying
// anything.
func scaffoldReqs(path string) error {
	reqsDir := filepath.Join(path, "reqs")
	if _, err := os.Stat(reqsDir); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", reqsDir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", reqsDir, err)
	}
	if err := os.MkdirAll(reqsDir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", reqsDir, err)
	}
	files := []struct {
		name    string
		content string
	}{
		{"OVERVIEW.md", skelOverview},
		{"INTERACTIVE.md", skelInteractive},
	}
	for _, f := range files {
		dest := filepath.Join(reqsDir, f.name)
		if err := os.WriteFile(dest, []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("write %q: %w", dest, err)
		}
	}
	return nil
}
