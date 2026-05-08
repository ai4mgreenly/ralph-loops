package loop

import (
	"context"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// loop consumes a [Spawner] it does not construct: the production
// implementation is provided by [agent.NewClaude] (wrapped via
// [NewClaudeSpawner] so the concrete agent type satisfies this
// package's [Spawner] interface). Tests inject their own [Spawner] /
// [Session] pair to drive a full run with no subprocess. The
// interfaces live in the consumer package because that's where they
// are used; the [agent] package only exposes concrete types.

// Spawner produces a [Session] for one iteration of the ralph loop.
// Implementations may share state (e.g. a binary path) across spawns
// but each Session is single-use.
type Spawner interface {
	Spawn(ctx context.Context, cfg agent.Config) (Session, error)
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
	//   - *[agent.ExitError] the process exited with a non-zero code.
	//   - any other error    I/O failure or signal death.
	//
	// Close is idempotent.
	Close() error
}

// claudeSpawnerAdapter wraps the concrete agent.Claude spawner so its
// Spawn method's return type lines up with the consumer-side Session
// interface declared in this package. Without this, Go's invariant
// return-type rule would prevent *agent.Claude from satisfying the
// Spawner interface directly.
type claudeSpawnerAdapter struct {
	c *agent.Claude
}

func (a claudeSpawnerAdapter) Spawn(ctx context.Context, cfg agent.Config) (Session, error) {
	return a.c.Spawn(ctx, cfg)
}

// newClaudeSpawner returns a Spawner backed by the production agent
// that runs the real `claude` CLI from $PATH.
func newClaudeSpawner() Spawner {
	return claudeSpawnerAdapter{c: agent.NewClaude()}
}
