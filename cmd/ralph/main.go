// Command ralph drives an iterative build agent (the "ralph loop")
// against a project's requirements directory.
//
// Run `ralph help` for a full operator manual. The minimal invocation
// is `ralph WORKDIR`, which uses default values for every flag.
package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
	"github.com/ai4mgreenly/ralph-loops/internal/loop"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// version is the build identifier reported by `ralph version` and
// stamped into the run banner.
const version = "0.1.0"

//go:embed prompt.md
var promptTemplate string

//go:embed skel/OVERVIEW.md
var skelOverview string

//go:embed skel/INTERACTIVE.md
var skelInteractive string

// Default values for every flag the loop subcommand accepts. Centralised
// here so the help text and the FlagSet stay in sync.
const (
	defaultReqs            = "reqs"
	defaultModel           = "opus"
	defaultEffort          = "medium"
	defaultDuration        = ""
	defaultConfigDir       = ""
	defaultTools           = ""
	defaultOneMContext     = true
	defaultClaudeAIMCP     = false
	defaultSkipPermissions = true
)

// Exit codes follow the convention used by Unix CLIs: 0 success, 1
// runtime error, 2 usage error.
const (
	exitSuccess = 0
	exitRuntime = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main's body in a testable shape: arguments come in, output
// goes to the supplied writers, and the exit status is returned rather
// than imposed via os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "init":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "ralph init: requires exactly one PATH argument")
			return exitUsage
		}
		if err := initSkeleton(args[1]); err != nil {
			fmt.Fprintf(stderr, "ralph: %s\n", err)
			return exitRuntime
		}
		return exitSuccess
	case "newid":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "ralph newid: takes no arguments")
			return exitUsage
		}
		fmt.Fprintln(stdout, idgen.New())
		return exitSuccess
	case "time-of":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "ralph time-of: requires exactly one ID argument")
			return exitUsage
		}
		t, err := idgen.TimeOf(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "ralph: %s\n", err)
			return exitUsage
		}
		fmt.Fprintln(stdout, t.UTC().Format("2006-01-02T15:04:05.000Z"))
		return exitSuccess
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "ralph %s\n", version)
		return exitSuccess
	case "help", "-h", "--help":
		writeUsagePaged(stdout)
		return exitSuccess
	default:
		return runLoop(args, stderr)
	}
}

// runLoop parses the loop subcommand's flags, materialises a
// [loop.Config], and hands off to [loop.Run].
func runLoop(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { writeUsage(stderr) }

	var (
		reqs      = fs.String("reqs", defaultReqs, "path to requirements directory")
		model     = fs.String("model", defaultModel, "haiku|sonnet|opus")
		effort    = fs.String("effort", defaultEffort, "low|medium|high|xhigh|max")
		duration  = fs.String("duration", defaultDuration, "wall-clock budget (e.g. 4h, 90m); empty means unlimited")
		configDir = fs.String("config-dir", defaultConfigDir, "value exported as CLAUDE_CONFIG_DIR; empty inherits claude's default (~/.claude)")
		oneM      = fs.Bool("1m-context", defaultOneMContext, "enable 1M-token context window")
		mcp       = fs.Bool("enable-claudeai-mcp-servers", defaultClaudeAIMCP, "enable Claude.ai-managed MCP servers")
		skipPerm  = fs.Bool("dangerously-skip-permissions", defaultSkipPermissions, "pass --dangerously-skip-permissions to claude")
		tools     = fs.String("tools", defaultTools, "comma-separated tool list; empty means all built-ins")
		verbose   = fs.Bool("verbose", false, "echo low-signal stream events (system init, rate_limit)")
	)

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "ralph: WORKDIR positional argument is required")
		writeUsage(stderr)
		return exitUsage
	}
	workdir := fs.Arg(0)

	prompt := strings.NewReplacer(
		"{{REQS}}", *reqs,
		"{{WORKDIR}}", workdir,
	).Replace(promptTemplate)

	cfg := loop.Config{
		ReqsDir:         *reqs,
		WorkDir:         workdir,
		Model:           *model,
		Effort:          *effort,
		Duration:        *duration,
		ConfigDir:       *configDir,
		OneMContext:     *oneM,
		ClaudeAIMCP:     *mcp,
		SkipPermissions: *skipPerm,
		Tools:           *tools,
		Prompt:          prompt,
		Version:         version,
		Verbose:         *verbose,
	}

	if err := loop.Run(cfg); err != nil {
		fmt.Fprintf(stderr, "ralph: %s\n", err)
		return exitRuntime
	}
	return exitSuccess
}

// writeUsagePaged writes the manual to stdout, routing through a
// pager when stdout is a terminal. The pager honors $PAGER if set
// (e.g. PAGER=cat short-circuits paging entirely); otherwise it
// falls back to `less -FRX`, whose -F flag means "quit if the
// content fits on one screen", so short manuals stay inline. Any
// pre-spawn failure (no pager binary, blocked StdinPipe) drops back
// to writing directly to stdout.
func writeUsagePaged(stdout io.Writer) {
	if !ui.IsTTY(stdout) {
		writeUsage(stdout)
		return
	}

	var argv []string
	if pager := os.Getenv("PAGER"); pager != "" {
		argv = strings.Fields(pager)
	} else {
		argv = []string{"less", "-FRX"}
	}
	if len(argv) == 0 {
		writeUsage(stdout)
		return
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdinPipe()
	if err != nil {
		writeUsage(stdout)
		return
	}
	if err := cmd.Start(); err != nil {
		writeUsage(stdout)
		return
	}
	writeUsage(pipe)
	_ = pipe.Close()
	_ = cmd.Wait()
}

// initSkeleton scaffolds PATH/reqs/ with the OVERVIEW.md and
// INTERACTIVE.md templates. PATH is created if missing; if PATH/reqs/
// already exists, the call refuses without modifying anything.
func initSkeleton(path string) error {
	reqsDir := filepath.Join(path, "reqs")
	if _, err := os.Stat(reqsDir); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", reqsDir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(reqsDir, 0o755); err != nil {
		return err
	}
	files := []struct {
		name    string
		content string
	}{
		{"OVERVIEW.md", skelOverview},
		{"INTERACTIVE.md", skelInteractive},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(reqsDir, f.name), []byte(f.content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeUsage emits the operator manual. It is intentionally
// self-contained — ralph carries no separate config file and no man
// page, so this text is the canonical reference.
func writeUsage(w io.Writer) {
	fmt.Fprintf(w, `ralph %s — iterative build-agent driver

USAGE
  ralph [flags] WORKDIR        Run the iteration loop against WORKDIR.
  ralph init PATH              Scaffold PATH/reqs/ with a starter spec
                               and a brief for an interactive helper agent.
  ralph newid                  Print a fresh requirement ID (R-XXXX-XXXX).
  ralph time-of ID             Print the UTC instant from which ID was minted.
  ralph version                Print version.
  ralph help                   Print this manual.

DESCRIPTION
  ralph spawns the claude CLI in a loop. Each iteration the agent reads
  the spec under --reqs, inspects the source tree at WORKDIR, makes one
  focused change, runs whatever tests the project defines, and reports
  back DONE or CONTINUE. The loop ends when the agent reports DONE or
  the wall-clock budget set by --duration expires.

  The minimal invocation

      ralph .

  uses every default below: it reads the spec from ./reqs/, builds in
  the current directory, calls opus at medium effort with the 1M-token
  context window enabled and permission prompts skipped, and runs until
  DONE with no time cap.

CONTRACT WITH THE AGENT
  --reqs is read-only to the agent. It is the operator's input; only
  the operator edits it (see the IDS section). WORKDIR is where the
  agent reads and writes — source, tests, build artifacts, scratch
  files. ralph never edits either tree itself.

FLAGS (loop subcommand)
  --reqs=PATH                          requirements directory
                                       (default: %q)
  --model=haiku|sonnet|opus            model alias (default: %q)
  --effort=low|medium|high|xhigh|max   effort level (default: %q)
  --duration=DURATION                  wall-clock budget, Go duration
                                       syntax: 30s, 90m, 4h, 1h30m.
                                       Empty = unlimited (default).
  --config-dir=PATH                    exported as CLAUDE_CONFIG_DIR.
                                       Empty inherits ~/.claude (default).
  --1m-context[=BOOL]                  1M-token context window
                                       (default: %t)
  --dangerously-skip-permissions[=BOOL]
                                       pass --dangerously-skip-permissions
                                       through to claude (default: %t)
  --enable-claudeai-mcp-servers[=BOOL]
                                       enable Claude.ai-managed MCP
                                       servers (default: %t)
  --tools=LIST                         pass --tools through to claude.
                                       Empty = all built-ins (default).
  --verbose[=BOOL]                     echo low-signal stream events
                                       (system init, rate_limit) (default: false)

  Boolean flags accept --flag, --flag=true, --flag=false. To turn off a
  default-true flag, write e.g. --1m-context=false.

EXAMPLES
  Build the app in the current directory, defaults:
      ralph .

  Custom budget and a different model:
      ralph --model=sonnet --duration=2h .

  Spec lives elsewhere, code in ./app:
      ralph --reqs=../shared-spec ./app

  Disable a default-on flag:
      ralph --1m-context=false .

REQUIREMENT IDS
  Spec bullets carry IDs of the form R-XXXX-XXXX. Each ID encodes the
  millisecond at which it was minted (since 2025-01-01 UTC) through an
  invertible scrambler so adjacent IDs look uncorrelated.

      ralph newid                  prints a fresh ID
      ralph time-of R-XXXX-XXXX    decodes one back to its UTC instant

  Embed the ID in source comments and test names so the spec stays
  traceable:

      // R-052Y-EKE0: only registered users may post.
      func TestR_052Y_EKE0_AnonymousPostIsRejected(t *testing.T) { ... }

  Dashes are replaced with underscores inside Go test names so the
  result is a valid identifier.
`,
		version,
		defaultReqs,
		defaultModel,
		defaultEffort,
		defaultOneMContext,
		defaultSkipPermissions,
		defaultClaudeAIMCP,
	)
}
