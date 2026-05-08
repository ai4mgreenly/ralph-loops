// Package agent wraps the claude CLI as a long-lived child process
// behind a Spawner / Session pair. The driver in [internal/loop]
// depends on these interfaces and is therefore decoupled from
// [os/exec], the wire-format envelope, and process-group plumbing.
//
// Production code calls [NewClaude] to obtain a Spawner that runs the
// real `claude` binary. Tests can supply their own Spawner — typically
// one whose Session yields a stream.Reader over canned bytes — and
// drive the loop with no subprocess at all.
package agent

import (
	"context"
	"fmt"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// Config captures the per-spawn knobs ralph forwards to the claude
// CLI. It is intentionally smaller than loop.Config: nothing here
// concerns the operator prompt, wall-clock budget, or output-rendering
// choices, all of which the loop owns.
type Config struct {
	// Model is one of "haiku", "sonnet", or "opus".
	Model string

	// Effort is one of "low", "medium", "high", "xhigh", or "max".
	Effort string

	// Tools, if non-empty, is forwarded verbatim to claude --tools.
	Tools string

	// SkipPermissions passes --dangerously-skip-permissions when true.
	SkipPermissions bool

	// ConfigDir, if non-empty, is exported as CLAUDE_CONFIG_DIR. An
	// empty string leaves the env var unset so claude uses ~/.claude.
	ConfigDir string

	// OneMContext enables the 1M-token context window when true.
	OneMContext bool

	// ClaudeAIMCP enables Claude.ai-managed MCP servers when true.
	ClaudeAIMCP bool

	// WorkDir is the working directory for the spawned process. The
	// agent reads and writes inside this tree.
	WorkDir string
}

// Spawner produces a [Session] for one iteration of the ralph loop.
// Implementations may share state (e.g. a binary path) across spawns
// but each Session is single-use.
type Spawner interface {
	Spawn(ctx context.Context, cfg Config) (Session, error)
}

// Session is one running agent process. The lifecycle is:
//
//  1. [Session.Send] one or more user messages.
//  2. Read events from [Session.Events] until a result arrives.
//  3. [Session.Close] to shut down stdin and reap the process.
//
// Cancelling the ctx passed to [Spawner.Spawn] sends SIGINT to the
// process group and (after a brief grace period) SIGKILL. Close then
// returns whatever exit information is available.
type Session interface {
	// Events returns the stream reader bound to the agent's stdout.
	// The same reader is returned on every call.
	Events() *stream.Reader

	// Send writes a single user-message envelope to the agent's
	// stdin, followed by a newline (the stream-json line framing).
	Send(text string) error

	// Close closes stdin so the agent can exit cleanly, waits for
	// the process to reap, and returns the outcome:
	//
	//   - nil               the process exited 0 (clean).
	//   - *[ExitError]      the process exited with a non-zero code.
	//   - any other error   I/O failure or signal death.
	//
	// Close is idempotent.
	Close() error
}

// ExitError reports a non-zero exit from the agent process. The loop
// driver tolerates a small set of exit codes when a well-formed
// result was already observed; everything else bubbles up as a fatal
// iteration error.
type ExitError struct {
	Code int
}

// Error implements [error]. The message intentionally omits any
// underlying *exec.ExitError so callers that want the code go through
// the [ExitError.Code] field rather than parsing strings.
func (e *ExitError) Error() string {
	return fmt.Sprintf("agent exited with status %d", e.Code)
}
