// Package agent wraps the claude CLI as a long-lived child process.
// It exposes a [Spawner] type — produced by [NewSpawner] — together
// with the [Session] interface its Spawn method returns. Each Session
// owns one running claude process: its stdin pipe, its stream-json
// reader, and its lifecycle.
//
// Production code in [internal/loop] consumes a Spawner directly. Tests
// in that package supply their own Spawner / Session pair (typically a
// session that yields a stream.Reader over canned bytes) so the loop
// driver can be exercised with no subprocess at all.
package agent

import (
	"fmt"
	"syscall"

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

// Session is one running agent process. The lifecycle is:
//
//  1. [Session.Send] one or more user messages.
//  2. Read events from [Session.Events] until a result arrives.
//  3. [Session.Close] to shut down stdin and reap the process.
//
// Cancelling the context passed to [Spawner.Spawn] sends SIGINT to the
// process group and (after a brief grace period) SIGKILL. Close returns
// whatever exit information is available.
//
// Implementations are not safe for concurrent use; the loop drives a
// single Session from one goroutine.
type Session interface {
	// Events returns the stream reader bound to the agent's stdout.
	// The same reader is returned on every call.
	Events() *stream.Reader

	// Send writes a single user-message envelope to the agent's stdin,
	// followed by the protocol's required newline framing.
	Send(text string) error

	// Close closes stdin so the agent can exit cleanly, waits for the
	// process to reap, and returns the outcome:
	//
	//   - nil               the process exited 0 (clean).
	//   - *[ExitError]      the process exited with a non-zero code or
	//                       was killed by a signal.
	//   - any other error   I/O failure or other runtime issue.
	//
	// Close is idempotent: subsequent calls return the original result.
	Close() error
}

// ExitError reports a non-zero exit from the agent process. The loop
// driver tolerates a small set of exit codes when a well-formed result
// was already observed; everything else bubbles up as a fatal iteration
// error.
//
// When the process was terminated by a signal rather than a normal
// exit, Signaled is true and Signal carries the delivering signal
// (Code is then the conventional 128+signum, matching shell semantics).
type ExitError struct {
	Code     int
	Signaled bool
	Signal   syscall.Signal
}

// Error implements [error]. The message intentionally omits any
// underlying *exec.ExitError so callers that want the code go through
// the [ExitError.Code] field rather than parsing strings.
func (e *ExitError) Error() string {
	if e.Signaled {
		return fmt.Sprintf("agent killed by signal %s (status %d)", e.Signal, e.Code)
	}
	return fmt.Sprintf("agent exited with status %d", e.Code)
}
