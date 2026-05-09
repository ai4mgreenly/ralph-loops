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

// maxRetriesPerIteration bounds the number of correction round-trips
// before ralph gives up on the current iteration. Matches the Ruby
// driver.
const maxRetriesPerIteration = 3

// errBadStructuredOutput is returned when a result event's
// structured_output is missing or fails the schema. The outer retry
// loop catches it and re-prompts claude with a correction.
var errBadStructuredOutput = errors.New("invalid structured output")

// errStreamEnded is returned when claude's stdout closes before a
// result event arrives. This is fatal for the iteration; no retry
// can recover it.
var errStreamEnded = errors.New("claude stream ended without result")

// runIteration drives a single agent invocation: it asks the spawner
// for a fresh [Session], sends the kickoff prompt, dispatches events
// into the emitter, and applies the structured-output retry policy.
// It returns the final [stream.Status] on success, or an error if the
// iteration could not complete.
//
// Cancellation of ctx (timeout or SIGINT) propagates through the
// spawner's exec.CommandContext wiring, which delivers SIGINT to the
// child's process group before the grace period elapses.
func runIteration(ctx context.Context, cfg Config, o options, sp Spawner, e *render.Emitter, s *stats) (status stream.Status, err error) {
	sess, err := sp.Spawn(ctx, agentConfig(cfg, o))
	if err != nil {
		return stream.StatusUnknown, err
	}

	// defer Close so a panic in pumpStream doesn't leak the child. The
	// named return lets us merge the Close error with whatever we have.
	var closeErr error
	defer func() {
		closeErr = sess.Close()

		// Surface ctx errors first because they tell the operator
		// whether the run was interrupted vs. naturally completed.
		if cErr := ctx.Err(); cErr != nil {
			// Preserve closeErr alongside the cancellation cause: a
			// signaled child still has useful exit info.
			err = errors.Join(cErr, closeErr)
			status = stream.StatusUnknown
			return
		}
		if err != nil {
			return
		}
		if closeErr != nil {
			// Narrow the failure-tolerance window to documented cases:
			// claude is known to exit 0 or 1 even when the iteration
			// produced a well-formed result. Anything else (signal death,
			// exit codes >1) bubbles up.
			var ee *agent.ExitError
			if errors.As(closeErr, &ee) {
				if !ee.Signaled && (ee.Code == 0 || ee.Code == 1) && status != stream.StatusUnknown {
					return
				}
				err = fmt.Errorf("claude exited with status %d: %w", ee.Code, ee)
				status = stream.StatusUnknown
				return
			}
			err = fmt.Errorf("claude exited: %w", closeErr)
			status = stream.StatusUnknown
		}
	}()

	e.ResetIteration()
	status, err = pumpStream(ctx, sess, e, s, cfg.Prompt)
	return status, err
}

// agentConfig projects the loop Config plus its options down to the
// subset the agent package consumes. Loop-level concerns (Prompt,
// Duration, Verbose, Version) intentionally do not cross the boundary.
func agentConfig(cfg Config, o options) agent.Config {
	return agent.Config{
		Model:           o.model,
		Effort:          o.effort,
		Tools:           o.tools,
		SkipPermissions: o.skipPermissions,
		ConfigDir:       o.configDir,
		OneMContext:     o.oneMContext,
		ClaudeAIMCP:     o.claudeAIMCP,
		WorkDir:         cfg.WorkDir,
	}
}

// pumpStream sends the kickoff message, then alternates between
// reading events and (on a malformed result) sending corrections, up
// to [maxRetriesPerIteration] times. ctx is honored between retry
// attempts so an operator interrupt isn't held up by a queued
// correction round.
func pumpStream(
	ctx context.Context,
	sess Session,
	e *render.Emitter,
	s *stats,
	prompt string,
) (stream.Status, error) {
	if err := sess.Send(prompt); err != nil {
		return stream.StatusUnknown, fmt.Errorf("send kickoff: %w", err)
	}

	r := sess.Events()
	for retry := 0; ; retry++ {
		status, err := readUntilResult(r, e, s)
		if err == nil {
			return status, nil
		}
		if !errors.Is(err, errBadStructuredOutput) {
			return stream.StatusUnknown, err
		}
		if retry >= maxRetriesPerIteration {
			return stream.StatusUnknown, fmt.Errorf("%w after %d retries", err, retry)
		}
		// Respect cancellation between attempts. The scanner itself
		// isn't context-aware, but at least the retry loop won't queue
		// another correction once the operator has hit Ctrl-C.
		select {
		case <-ctx.Done():
			return stream.StatusUnknown, ctx.Err()
		default:
		}
		if cErr := sess.Send(correctionMessage(err)); cErr != nil {
			return stream.StatusUnknown, fmt.Errorf("send correction: %w", cErr)
		}
	}
}

// readUntilResult drains r until a result event arrives. Each event
// is dispatched into the emitter and tallied in stats. A missing or
// malformed structured_output returns [errBadStructuredOutput] so the
// caller can retry. Unrecognised event types and unparseable lines
// are surfaced verbatim and decoding resumes on the next line, so a
// new event kind from claude does not abort the iteration.
func readUntilResult(r *stream.Reader, e *render.Emitter, s *stats) (stream.Status, error) {
	for {
		e.Spinner().Start()
		ev, err := r.Next()
		e.Spinner().Stop()
		if errors.Is(err, io.EOF) {
			return stream.StatusUnknown, errStreamEnded
		}
		if err != nil {
			// Forward-compat and resilience: log the offending line so
			// the operator retains full visibility, then keep reading.
			var de *stream.DecodeError
			if errors.As(err, &de) {
				e.OnDecodeError(*de)
				continue
			}
			return stream.StatusUnknown, fmt.Errorf("read stream: %w", err)
		}

		s.tallyEvent(ev.Kind())

		switch ev := ev.(type) {
		case stream.Assistant:
			e.OnAssistant(ev)
		case stream.User:
			e.OnUser(ev)
		case stream.Result:
			e.OnResult(ev)
			label := render.DecodeStatus(ev.StructuredOutput)
			status, ok := stream.ParseStatus(label)
			if !ok {
				return stream.StatusUnknown, errBadStructuredOutput
			}
			return status, nil
		case stream.System:
			e.OnSystem(ev)
		case stream.RateLimit:
			e.OnRateLimit(ev)
		case stream.UnknownEvent:
			// Already tallied; the bad-type error path can't reach
			// here because Reader.Next pairs unknowns with an error.
		}
	}
}

// correctionMessage produces the natural-language nudge sent to
// claude after a malformed result event. The text intentionally
// names the schema requirement so the model has the information it
// needs to comply on the next turn.
func correctionMessage(cause error) string {
	return fmt.Sprintf(
		"Your previous reply did not satisfy the required structured output (%v). "+
			"Reply again, this time using the StructuredOutput tool exactly once with "+
			`{"status":"DONE"} or {"status":"CONTINUE"}.`,
		cause,
	)
}
