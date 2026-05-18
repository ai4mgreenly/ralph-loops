// Package agent wraps the `pi` CLI as a one-shot child process. ralph
// drives pi exclusively: every iteration spawns `pi -p --mode json`
// once, feeds it the kickoff prompt as the trailing positional
// argument, reads pi's native JSONL event stream until `agent_end`,
// and lets the process exit. The package exposes a [Spawner] type —
// produced by [NewSpawner] — together with the [Session] interface its
// Spawn method returns. Each Session owns one running pi process: its
// stdout stream reader and its lifecycle.
//
// Production code in [internal/loop] consumes a Spawner directly. Tests
// in that package supply their own Spawner / Session pair (typically a
// session that yields a stream.Reader over canned bytes) so the loop
// driver can be exercised with no subprocess at all. That is why the
// [Spawner]/[Session] seam survives even though pi is the only engine:
// it is the test seam, not multi-engine machinery.
package agent

import (
	"fmt"
	"syscall"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// Config captures the per-spawn knobs ralph forwards to the pi CLI. It
// is intentionally smaller than loop.Config: nothing here concerns the
// wall-clock budget or output-rendering choices, all of which the loop
// owns. Every field maps directly to a pi argv token.
type Config struct {
	// Prompt is the kickoff message for this iteration. It is passed to
	// pi as the trailing positional argument (pi's `-p` consumes the
	// next non-flag token as the prompt). pi has no stdin user-message
	// protocol, so this is the only way to deliver the kickoff; an empty
	// Prompt would leave pi with nothing to do.
	Prompt string

	// SystemPromptFile is the ABSOLUTE path to the build-agent
	// AGENTS.md. It is forwarded as `--append-system-prompt
	// <SystemPromptFile>`: pi appends the file's contents to its base
	// system prompt. The persona is injected this way (not discovered
	// via AGENTS.md walk-up, which `--no-context-files` suppresses
	// entirely), so the build agent sees exactly this file and nothing
	// else. The loop computes the absolute path from its WorkDir /
	// app-root; this package does not do walk-up.
	SystemPromptFile string

	// Provider, if non-empty, is forwarded as `--provider <Provider>`.
	// ralph has no default: when empty the flag is omitted and pi falls
	// back to its own ~/.pi/agent/settings.json default.
	Provider string

	// Model, if non-empty, is forwarded as `--model <Model>`. ralph does
	// not parse it: pi's `provider/id` and `model:thinking` forms are
	// passed through opaque. When empty the flag is omitted and pi uses
	// its own default.
	Model string

	// Thinking, if non-empty, is forwarded as `--thinking <Thinking>`.
	// pi validates the level itself (off|minimal|low|medium|high|xhigh)
	// — ralph applies no mapping table. When empty the flag is omitted
	// and pi uses its own default.
	Thinking string

	// Tools, if non-empty, replaces the default built-in allowlist as
	// the value of `--tools`. An empty value yields ralph's default
	// allowlist of all seven pi built-ins (see [defaultTools]); a
	// non-empty operator-supplied value narrows (or otherwise overrides)
	// it verbatim.
	Tools string

	// WorkDir is the working directory for the spawned process. The
	// agent reads and writes inside this tree (the project's app-root).
	WorkDir string

	// Raw, when true, appends `--raw` to pi's argv. The flag is ralph's
	// engine-neutral operator-debug convention: combined with the
	// Spawner's Stdout tap it dumps pi's verbatim `-p --mode json` JSONL
	// with no ralph decoration. It is not a compatibility shim — it is
	// the best window into the stream decoder while that layer is being
	// built.
	Raw bool
}

// Session is one running pi process. The lifecycle is:
//
//  1. Read events from [Session.Events] until [stream.AgentEnd] (or
//     EOF / a fatal error).
//  2. [Session.Close] to reap the process.
//
// pi runs in one-shot print mode: the prompt was delivered as the argv
// positional at spawn and the process exits after emitting `agent_end`.
// There is therefore nothing to inject into a running pi; [Session.Send]
// is retained only for the test seam (fakes feed canned JSONL through
// it) and is a documented no-op in the production implementation.
//
// Cancelling the context passed to [Spawner.Spawn] sends SIGTERM to the
// process group and (after a brief grace period) SIGKILL. Close returns
// whatever exit information is available.
//
// Implementations are not safe for concurrent use; the loop drives a
// single Session from one goroutine.
type Session interface {
	// Events returns the stream reader bound to pi's stdout. The same
	// reader is returned on every call.
	Events() *stream.Reader

	// Send exists for the test seam only. In the production pi Session
	// it is a no-op that returns nil: pi runs one-shot with the prompt
	// already passed as the argv positional at spawn, so nothing can be
	// injected into the exiting process. Fakes in the loop package
	// implement Send to drive canned JSONL through a real stream.Reader.
	Send(text string) error

	// Close waits for the process to reap and returns the outcome:
	//
	//   - nil               the process exited 0 (clean).
	//   - *[ExitError]      the process exited with a non-zero code or
	//                       was killed by a signal.
	//   - any other error   I/O failure or other runtime issue.
	//
	// Close is idempotent: subsequent calls return the original result.
	Close() error
}

// ExitError reports a non-zero exit from the pi process. It is
// advisory/diagnostic only: under the pi migration the iteration
// outcome is event-driven (the loop decides from the observed
// `agent_end` plus the RALPH-STATUS sentinel), not exit-code-driven.
// The conventional codes are 0 clean, 1 startup-or-turn error, 143
// SIGTERM, 129 SIGHUP, ~130 abrupt; none of them change ralph's
// control flow on their own.
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
