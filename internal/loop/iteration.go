package loop

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// errStreamEnded is returned when pi's stdout closes before a terminal
// [stream.AgentEnd] arrives. Under the pi migration this is the
// iteration-error case (Q3): pi always ends a healthy run with
// agent_end, so reaching EOF without one means the process died
// mid-turn. There is no in-process correction — the loop is safely
// repeatable, so a fresh iteration is the recovery.
var errStreamEnded = errors.New("pi stream ended without agent_end")

// runIteration drives a single pi invocation. It asks the spawner for a
// fresh one-shot [Session] (the kickoff prompt is already baked into
// the spawned argv via [agent.Config.Prompt]), pumps the event stream
// once to the terminal [stream.AgentEnd], and returns the parsed
// [stream.Status]. There is no correction retry: pi runs one-shot and
// cannot be re-prompted, and the loop is safely repeatable.
//
// Cancellation of ctx (timeout or SIGINT) propagates through the
// spawner's exec wiring, which delivers SIGTERM to the child's process
// group before the grace period elapses. The [agent.ExitError] a Close
// may surface is advisory/diagnostic only (Q9): the iteration outcome
// is decided entirely from the observed agent_end, never the exit code.
func runIteration(ctx context.Context, cfg Config, o options, sp Spawner, e *render.Emitter, s *stats) (status stream.Status, err error) {
	sess, err := sp.Spawn(ctx, agentConfig(cfg, o))
	if err != nil {
		return stream.StatusUnknown, err
	}

	// defer Close so a panic in pumpStream doesn't leak the child. The
	// named return lets us fold the Close error into whatever we have.
	defer func() {
		closeErr := sess.Close()

		// ctx errors take precedence: they tell the operator the run
		// was interrupted/timed out rather than naturally completing.
		if cErr := ctx.Err(); cErr != nil {
			err = errors.Join(cErr, closeErr)
			status = stream.StatusUnknown
			return
		}
		if err != nil {
			return
		}
		// Close's *agent.ExitError is advisory only under the pi
		// migration (Q9): the outcome was already decided from the
		// observed agent_end, so a non-zero exit does not override a
		// committed DONE/CONTINUE. A non-ExitError Close failure (an
		// I/O fault) is still worth surfacing.
		if closeErr != nil {
			var ee *agent.ExitError
			if !errors.As(closeErr, &ee) {
				err = fmt.Errorf("pi session close: %w", closeErr)
				status = stream.StatusUnknown
			}
		}
	}()

	e.ResetIteration()
	status, err = pumpStream(ctx, sess, e, s)
	return status, err
}

// agentConfig projects the loop Config plus its options down to the
// subset the [agent] package consumes. The kickoff prompt rides in
// [agent.Config.Prompt] (pi's trailing positional argv) and the
// build-agent persona rides in SystemPromptFile (an absolute path to
// the app-root AGENTS.md, computed by the caller). Provider and
// Thinking are plumbed through so a later slice can add cmd flags; for
// now they default to zero, leaving pi to use its own settings. Loop-
// level concerns (Duration, Verbose) intentionally do not cross the
// boundary.
func agentConfig(cfg Config, o options) agent.Config {
	return agent.Config{
		Prompt:           cfg.Prompt,
		SystemPromptFile: cfg.SystemPromptFile,
		Provider:         o.provider,
		Model:            o.model,
		Thinking:         o.thinking,
		Tools:            o.tools,
		WorkDir:          cfg.WorkDir,
		Raw:              o.raw,
	}
}

// pumpStream drains the session's event stream once, dispatching every
// event into the emitter and tallying it in stats, until the terminal
// [stream.AgentEnd] arrives. On agent_end the per-iteration token/cost
// tally is folded into the run total and the DONE/CONTINUE sentinel is
// computed via [stream.StatusFromAgentEnd] (which already returns
// [stream.StatusContinue] as the safe default when an agent_end is
// present but carries no parseable sentinel — Q3).
//
// If the stream reaches EOF with no agent_end observed the iteration
// failed: [errStreamEnded] is returned and the caller decides between
// ctx-cancelled (abort) and a plain iteration error. Decode errors are
// surfaced through the emitter and decoding resumes, so a novel pi
// event type does not abort the iteration.
func pumpStream(
	ctx context.Context,
	sess Session,
	e *render.Emitter,
	s *stats,
) (stream.Status, error) {
	r := sess.Events()
	for {
		// Honor cancellation between reads so an operator interrupt is
		// not held up by a long-running event pump. The scanner itself
		// is not context-aware, but the spawner's Cmd.Cancel delivers
		// SIGTERM to pi which closes the stream and surfaces here.
		if cErr := ctx.Err(); cErr != nil {
			return stream.StatusUnknown, cErr
		}

		e.Spinner().Start()
		ev, rErr := r.Next()
		e.Spinner().Stop()

		if errors.Is(rErr, io.EOF) {
			// Clean EOF with no agent_end: pi died mid-turn. The stream
			// package never fabricates an agent_end, so this is the
			// authoritative "no terminal" signal.
			return stream.StatusUnknown, errStreamEnded
		}
		if rErr != nil {
			// Forward-compat and resilience: surface the offending line
			// through the emitter, tally the carrier if one was
			// returned, then keep reading. An unknown pi type comes
			// paired with a non-nil event (UnknownEvent) plus a
			// DecodeError; a malformed line comes with a nil event.
			var de *stream.DecodeError
			if errors.As(rErr, &de) {
				e.OnDecodeError(*de)
				if ev != nil {
					e.OnEvent(ev)
				}
				continue
			}
			return stream.StatusUnknown, fmt.Errorf("read stream: %w", rErr)
		}

		// agent_end is the terminal event and the single source of
		// truth for the iteration tally and status (Q6/Q3). Fold the
		// per-turn usages into the run total, then decide the outcome.
		if ae, ok := ev.(stream.AgentEnd); ok {
			e.OnEvent(ae)
			s.tallyAgentEnd(ae)
			return stream.StatusFromAgentEnd(ae), nil
		}

		e.OnEvent(ev)
	}
}
