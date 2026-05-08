// Package agent wraps the claude CLI as a long-lived child process.
// It exposes concrete types — [Claude] and its session — that the
// driver in [internal/loop] consumes through its own Spawner / Session
// interfaces. Putting the interface declarations on the consumer side
// (loop, not agent) follows the standard Go convention.
//
// Production code calls [NewClaude] to obtain a [*Claude] that runs
// the real `claude` binary. Tests in the loop package supply their
// own Spawner / Session pair — typically one whose Session yields a
// stream.Reader over canned bytes — and drive the loop with no
// subprocess at all.
package agent

import "fmt"

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
