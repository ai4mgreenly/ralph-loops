package loop

import (
	"context"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
)

// The loop consumes a [Spawner] it does not construct. Production code
// uses [agent.NewSpawner]; tests inject their own Spawner / [Session]
// pair to drive a full run with no subprocess.
//
// We keep the [Spawner] interface in this consumer package — that is
// the canonical "interfaces at the consumer" arrangement. [Session] is
// a re-export of [agent.Session] (defined there because the spawner's
// concrete return type needs an interface to widen onto): aliasing it
// here keeps loop's public vocabulary self-contained while letting
// *agent.Spawner directly satisfy [Spawner] with no adapter.

// Spawner produces a [Session] for one iteration of the ralph loop.
// Implementations may share state (e.g. a binary path) across spawns
// but each Session is single-use.
type Spawner interface {
	Spawn(ctx context.Context, cfg agent.Config) (Session, error)
}

// Session is one running agent process. See [agent.Session] for the
// full contract; this alias lets callers reference loop.Session without
// importing the agent package directly.
type Session = agent.Session
