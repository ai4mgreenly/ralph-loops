package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// runReset parses `ralph reset`'s flags and restores the app-root
// directory to the virgin state produced by `ralph init`: every entry
// inside it is removed (including .ralph/ state and any stray .git),
// then the templated build-agent AGENTS.md is written back.
//
// Project-level git is the safety net. There is no prompt, no --force,
// no backup — the operator already opted in by typing the subcommand,
// and `git status` afterward shows exactly what was destroyed.
//
// Refuses unless the cwd looks like a ralph project root: the spec,
// app, and helper subdirectories must all exist as directories. Run
// from the project root (the directory containing helper/, reqs/, and
// app-root/), or pass that directory as PROJECT_ROOT.
func runReset(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph reset", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reqsName := fs.String("reqs", defaultInitReqsName, "name of the spec subdirectory")
	appRootName := fs.String("app-root", defaultInitAppRootName, "name of the application source subdirectory")
	helperName := fs.String("helper", defaultInitHelperName, "name of the spec-helper subdirectory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "ralph reset: at most one positional argument (PROJECT_ROOT)")
		return exitUsage
	}
	if *reqsName == "" || *appRootName == "" || *helperName == "" {
		fmt.Fprintln(stderr, "ralph reset: --reqs, --app-root, and --helper must not be empty")
		return exitUsage
	}
	if strings.ContainsAny(*reqsName, "/\\") ||
		strings.ContainsAny(*appRootName, "/\\") ||
		strings.ContainsAny(*helperName, "/\\") {
		fmt.Fprintln(stderr, "ralph reset: --reqs, --app-root, and --helper must be plain directory names, not paths")
		return exitUsage
	}
	if *reqsName == *appRootName || *reqsName == *helperName || *appRootName == *helperName {
		fmt.Fprintln(stderr, "ralph reset: --reqs, --app-root, and --helper must all differ")
		return exitUsage
	}

	projectRoot := "."
	if fs.NArg() == 1 {
		projectRoot = fs.Arg(0)
	}
	if err := os.Chdir(projectRoot); err != nil {
		fmt.Fprintf(stderr, "ralph: chdir %q: %s\n", projectRoot, err)
		return exitUsage
	}

	if err := resetAppRoot(*reqsName, *appRootName, *helperName); err != nil {
		fmt.Fprintf(stderr, "ralph: %s\n", err)
		return exitRuntime
	}
	return exitSuccess
}

// resetAppRoot performs the actual wipe-and-rewrite. The project-shape
// check fires first — if any of the three subdirectories is missing
// or not a directory, nothing is touched. Otherwise every entry inside
// app-root/ is removed via os.RemoveAll (so .ralph/, generated source,
// nested .git, and anything else go), then a freshly-rendered
// AGENTS.md is written.
func resetAppRoot(reqsName, appRootName, helperName string) error {
	for _, name := range []string{reqsName, appRootName, helperName} {
		info, err := os.Lstat(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("not a ralph project root: missing %s/", name)
			}
			return fmt.Errorf("stat %s: %w", name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("not a ralph project root: %s is not a directory", name)
		}
	}

	entries, err := os.ReadDir(appRootName)
	if err != nil {
		return fmt.Errorf("read %s: %w", appRootName, err)
	}
	for _, e := range entries {
		p := filepath.Join(appRootName, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}

	target := filepath.Join(appRootName, "AGENTS.md")
	body := renderSkel(skelAgentsApp, reqsName, appRootName, helperName)
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}
