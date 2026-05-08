package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// UsageDefaults carries the default flag values shown in the operator
// manual. The CLI passes these in so the help text and the flag
// definitions stay in sync without making either side a circular
// import target.
type UsageDefaults struct {
	Version         string
	Reqs            string
	Model           string
	Effort          string
	OneMContext     bool
	SkipPermissions bool
	ClaudeAIMCP     bool
	OutputLines     int
}

// WriteUsage emits the operator manual to w. It is intentionally
// self-contained — ralph carries no separate config file and no man
// page, so this text is the canonical reference.
func WriteUsage(w io.Writer, d UsageDefaults) {
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
  --output-lines=N                     max lines of tool output (Bash
                                       stdout/stderr, Read contents,
                                       Edit/Write hunks) replayed per
                                       result before a '...' truncation
                                       marker (default: %d)

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
		d.Version,
		d.Reqs,
		d.Model,
		d.Effort,
		d.OneMContext,
		d.SkipPermissions,
		d.ClaudeAIMCP,
		d.OutputLines,
	)
}

// WriteUsagePaged writes the manual to stdout, routing through a
// pager when stdout is a terminal. The pager honors $PAGER if set
// (e.g. PAGER=cat short-circuits paging entirely); otherwise it
// falls back to `less -FRX`, whose -F flag means "quit if the
// content fits on one screen", so short manuals stay inline. Any
// pre-spawn failure (no pager binary, blocked StdinPipe) drops back
// to writing directly to stdout.
func WriteUsagePaged(stdout io.Writer, d UsageDefaults) {
	if !ui.IsTTY(stdout) {
		WriteUsage(stdout, d)
		return
	}

	var argv []string
	if pager := os.Getenv("PAGER"); pager != "" {
		argv = strings.Fields(pager)
	} else {
		argv = []string{"less", "-FRX"}
	}
	if len(argv) == 0 {
		WriteUsage(stdout, d)
		return
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdinPipe()
	if err != nil {
		WriteUsage(stdout, d)
		return
	}
	if err := cmd.Start(); err != nil {
		WriteUsage(stdout, d)
		return
	}
	WriteUsage(pipe, d)
	_ = pipe.Close()
	_ = cmd.Wait()
}
