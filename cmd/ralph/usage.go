package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// writeUsage emits the operator manual to w. It reads its defaults
// directly from this package's flag-default constants so the help
// text and the flag definitions stay in sync.
func writeUsage(w io.Writer) {
	fmt.Fprintf(w, `ralph %s — iterative build-agent driver

USAGE
  ralph [flags] [PROJECT_ROOT] Run the iteration loop. Run from the
                               project root (the directory containing
                               helper/, reqs/, and app-root/). The
                               optional PROJECT_ROOT positional
                               defaults to "." and ralph chdirs there
                               before doing anything else.
  ralph init [flags] PATH      Scaffold a new project tree under PATH.
  ralph newid [--number=N|-n N]
                               Print N fresh requirement IDs (R-XXXX-XXXX),
                               one per line. Default N=1. Each ID is
                               anchored to a distinct elapsed millisecond,
                               so N IDs take at least ~N-1 ms.
  ralph time-of ID             Print the UTC instant from which ID was minted.
  ralph unverified             Print a JSON report of the IDs in --reqs not
                               yet recorded in ./.ralph/requirements-verified.jsonl.
                               Invoked from inside the app-root.
  ralph version                Print version.
  ralph help                   Print this manual.

DESCRIPTION
  ralph spawns an engine CLI in a loop. The engine is the binary that
  implements claude's stream-json wire contract — by default the
  claude CLI itself, or any drop-in replacement passed via --engine.
  Each iteration the agent reads the spec under --reqs, inspects the
  current app-root, makes one focused change, runs whatever tests the
  project defines, and reports back DONE or CONTINUE. The loop ends
  when the agent reports DONE or the wall-clock budget set by
  --duration expires.

PROJECT LAYOUT
  `+"`ralph init my-app`"+` produces:

      my-app/
      ├── helper/                spec helper's workspace
      │   └── AGENTS.md          spec-helper instructions
      ├── reqs/                  the spec (operator-authored)
      │   └── OVERVIEW.md
      └── app-root/              the build root
          ├── AGENTS.md          build-agent instructions
          └── .ralph/            created on first run

  The two AGENTS.md files are the standing personas — the spec helper
  in my-app/helper/ and the build agent in my-app/app-root/. Each is
  auto-loaded by claude (and other AGENTS.md-aware engines) when a
  session starts in the matching directory. ralph itself runs from
  the project root and spawns the agent with cwd set to app-root/.

CONTRACT WITH THE AGENT
  --reqs is read-only to the agent. It is the operator's input; only
  the operator edits it (see the IDS section). The app-root
  subdirectory is where the agent reads and writes — source, tests,
  build artifacts, scratch files. ralph never edits either tree itself.

FLAGS (loop subcommand)
  --reqs=PATH                          spec directory, relative to the
                                       project root
                                       (default: %q)
  --app-root=PATH                      application source subdirectory,
                                       relative to the project root
                                       (default: %q)
  --engine=COMMAND                     engine command (claude drop-in
                                       replacement) resolved via $PATH;
                                       must implement claude's
                                       stream-json contract
                                       (default: %q)
  --model=NAME                         model alias forwarded to the
                                       engine. Must have a pricing
                                       entry in internal/pricing —
                                       ralph rejects unknown models at
                                       startup so operators are not
                                       surprised by a $0.0000 cost
                                       report on an untracked model.
                                       Adding a new model is one row
                                       in pricing.go (default: %q).
  --effort=NAME                        effort level forwarded to the
                                       engine. Engine-specific
                                       (e.g. low|medium|high|xhigh|max
                                       for claude); ralph does not
                                       validate (default: %q).
  --duration=DURATION                  wall-clock budget, Go duration
                                       syntax: 30s, 90m, 4h, 1h30m.
                                       Empty = unlimited (default).
  --config-dir=PATH                    exported as CLAUDE_CONFIG_DIR.
                                       Empty inherits ~/.claude (default).
  --1m-context[=BOOL]                  1M-token context window
                                       (default: %t)
  --dangerously-skip-permissions[=BOOL]
                                       pass --dangerously-skip-permissions
                                       through to the engine (default: %t)
  --enable-claudeai-mcp-servers[=BOOL]
                                       enable Claude.ai-managed MCP
                                       servers (default: %t)
  --tools=LIST                         pass --tools through to the engine.
                                       Empty = all built-ins (default).
  --verbose[=BOOL]                     echo low-signal stream events
                                       (system init, rate_limit) (default: false)
  --raw[=BOOL]                         debug passthrough: dump the
                                       engine's stdout verbatim as
                                       JSONL (prefixed with a
                                       _ralph_kickoff envelope describing
                                       the prompt), suppress every
                                       decorator, run exactly one
                                       iteration. Use to capture a
                                       diagnosable wire trace from an
                                       alternate engine (default: false).
  --output-lines=N                     max lines of tool output (Bash
                                       stdout/stderr, Read contents,
                                       Edit/Write hunks) replayed per
                                       result before a '...' truncation
                                       marker (default: %d)

  Boolean flags accept --flag, --flag=true, --flag=false. To turn off a
  default-true flag, write e.g. --1m-context=false.

FLAGS (init subcommand)
  --reqs=NAME                          name of the spec subdirectory
                                       (default: "reqs")
  --app-root=NAME                      name of the application source
                                       subdirectory (default: "app-root")
  --helper=NAME                        name of the spec-helper
                                       subdirectory (default: "helper")

  All three names are baked into the templated AGENTS.md files at
  scaffold time. After init, the layout is fixed; ralph at runtime
  infers everything from the cwd.

EXAMPLES
  Scaffold and run with defaults:
      ralph init my-app
      cd my-app
      ralph

  Custom budget and a different model:
      ralph --model=opus --duration=2h

  Disable a default-on flag:
      ralph --1m-context=false

  Run against a project without cd'ing into it first:
      ralph --duration=2h /path/to/project

  Custom subdir names at scaffold time:
      ralph init --reqs=spec --app-root=build --helper=designer my-app

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
		defaultAppRoot,
		defaultEngine,
		defaultModel,
		defaultEffort,
		defaultOneMContext,
		defaultSkipPermissions,
		defaultClaudeAIMCP,
		defaultOutputLines,
	)
}

// writeUsagePaged writes the manual to stdout, routing through a pager
// when stdout is a terminal. The pager honors $PAGER if set
// (e.g. PAGER=cat short-circuits paging entirely); otherwise it falls
// back to `less -FRX`, whose -F flag means "quit if the content fits
// on one screen", so short manuals stay inline. Any pre-spawn failure
// (no pager binary, blocked StdinPipe) drops back to writing directly
// to stdout.
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
