package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skel/OVERVIEW.md
var skelOverview string

//go:embed skel/AGENTS-helper.md
var skelAgentsHelper string

//go:embed skel/AGENTS-app.md
var skelAgentsApp string

// Default subdir names scaffolded by `ralph init`. All three can be
// overridden via flags; the chosen names get baked into the templated
// AGENTS.md files at scaffold time, so no runtime templating remains.
const (
	defaultInitReqsName    = "reqs"
	defaultInitAppRootName = "app-root"
	defaultInitHelperName  = "helper"
)

// renderSkel substitutes the operator-chosen subdirectory names into a
// scaffold template. Shared by `ralph init` (which writes the initial
// AGENTS.md files) and `ralph reset` (which restores the build-agent
// AGENTS.md to its virgin state). Keeping the substitution in one
// place keeps both paths in lockstep.
func renderSkel(template, reqsName, appRootName, helperName string) string {
	return strings.NewReplacer(
		"{{REQS}}", reqsName,
		"{{APP_ROOT}}", appRootName,
		"{{HELPER}}", helperName,
	).Replace(template)
}

// runInit parses `ralph init`'s flags, validates the positional, and
// hands off to scaffoldProject. Subcommand-specific so it can carry its
// own --reqs / --app-root / --helper surface without polluting the loop
// FlagSet.
func runInit(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reqsName := fs.String("reqs", defaultInitReqsName, "name of the spec subdirectory")
	appRootName := fs.String("app-root", defaultInitAppRootName, "name of the application source subdirectory")
	helperName := fs.String("helper", defaultInitHelperName, "name of the spec-helper subdirectory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "ralph init: requires exactly one PATH argument")
		return exitUsage
	}
	if *reqsName == "" || *appRootName == "" || *helperName == "" {
		fmt.Fprintln(stderr, "ralph init: --reqs, --app-root, and --helper must not be empty")
		return exitUsage
	}
	if strings.ContainsAny(*reqsName, "/\\") ||
		strings.ContainsAny(*appRootName, "/\\") ||
		strings.ContainsAny(*helperName, "/\\") {
		fmt.Fprintln(stderr, "ralph init: --reqs, --app-root, and --helper must be plain directory names, not paths")
		return exitUsage
	}
	if *reqsName == *appRootName || *reqsName == *helperName || *appRootName == *helperName {
		fmt.Fprintln(stderr, "ralph init: --reqs, --app-root, and --helper must all differ")
		return exitUsage
	}
	if err := scaffoldProject(fs.Arg(0), *reqsName, *appRootName, *helperName); err != nil {
		fmt.Fprintf(stderr, "ralph: %s\n", err)
		return exitRuntime
	}
	return exitSuccess
}

// scaffoldProject creates the full ralph project tree under root:
//
//	root/<helperName>/AGENTS.md   spec-helper instructions (templated)
//	root/<reqsName>/OVERVIEW.md   spec stub
//	root/<appRootName>/AGENTS.md  build-agent instructions (templated)
//
// No AGENTS.md or CLAUDE.md is scaffolded at the project root itself,
// and no CLAUDE.md is scaffolded inside any of the subdirectories.
// ralph drives pi with --no-context-files and injects the build-agent
// persona explicitly via --append-system-prompt <appRootName>/AGENTS.md,
// so pi does no parent-directory AGENTS.md/CLAUDE.md walk-up — a stray
// root-level file could not leak into the build agent regardless. The
// directory split is plain role separation: the spec-helper persona
// lives in its own sibling directory (<helperName>/) where the human
// spec-author session is invoked (e.g. `cd my-app/helper && pi`),
// keeping it out of the build agent's <reqsName>/ read sweep.
//
// If any of those paths already exists the call refuses without
// modifying anything — partial scaffolds are worse than no scaffold,
// because the operator can't tell what survived their previous run.
func scaffoldProject(root, reqsName, appRootName, helperName string) error {
	reqsDir := filepath.Join(root, reqsName)
	appRootDir := filepath.Join(root, appRootName)
	helperDir := filepath.Join(root, helperName)
	helperAgents := filepath.Join(helperDir, "AGENTS.md")
	appAgents := filepath.Join(appRootDir, "AGENTS.md")
	overview := filepath.Join(reqsDir, "OVERVIEW.md")

	// Refuse if any target exists. Lstat catches symlinks too, so a
	// dangling symlink from a previous half-scaffold is still flagged.
	for _, p := range []string{reqsDir, appRootDir, helperDir, helperAgents, appAgents, overview} {
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite", p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %q: %w", p, err)
		}
	}

	if err := os.MkdirAll(reqsDir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", reqsDir, err)
	}
	if err := os.MkdirAll(appRootDir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", appRootDir, err)
	}
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", helperDir, err)
	}

	writes := []struct {
		path    string
		content string
	}{
		{overview, skelOverview},
		{helperAgents, renderSkel(skelAgentsHelper, reqsName, appRootName, helperName)},
		{appAgents, renderSkel(skelAgentsApp, reqsName, appRootName, helperName)},
	}
	for _, w := range writes {
		if err := os.WriteFile(w.path, []byte(w.content), 0o644); err != nil {
			return fmt.Errorf("write %q: %w", w.path, err)
		}
	}
	return nil
}
