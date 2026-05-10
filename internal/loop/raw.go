package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
)

// runRaw executes the debug passthrough enabled by [WithRaw]. It spawns
// one session, writes a kickoff envelope describing the prompt ralph is
// about to send, sends that prompt, then drains the event stream to EOF
// while the spawner's tap mirrors raw stdout bytes onto w. No
// rendering, retry, structured-output check, stats, or results.jsonl
// happens here — the goal is a verbatim wire trace fit to feed to an
// agent diagnosing an alternate engine.
func runRaw(ctx context.Context, cfg Config, o options, w io.Writer, sp Spawner) error {
	sess, err := sp.Spawn(ctx, agentConfig(cfg, o))
	if err != nil {
		return err
	}

	// Defer Close so a panic during the drain doesn't leak the child.
	// Close errors with a 0/1 exit are swallowed (matching the typed
	// iteration's tolerance window): the engine often exits 1 even after
	// a well-formed result, and in raw mode we have no parsed result to
	// gate that judgment on.
	defer func() {
		closeErr := sess.Close()
		if closeErr == nil || err != nil {
			return
		}
		var ee *agent.ExitError
		if errors.As(closeErr, &ee) && !ee.Signaled && (ee.Code == 0 || ee.Code == 1) {
			return
		}
		err = fmt.Errorf("engine exited: %w", closeErr)
	}()

	if kErr := writeKickoff(w, cfg.Prompt); kErr != nil {
		return fmt.Errorf("write kickoff: %w", kErr)
	}
	if sErr := sess.Send(cfg.Prompt); sErr != nil {
		return fmt.Errorf("send kickoff: %w", sErr)
	}

	// Drain events to EOF. The reader's bytes flow through the spawner's
	// stdout tap, so we discard the parsed events themselves; decode
	// errors are equally uninteresting in raw mode (the malformed line
	// has already been teed verbatim).
	r := sess.Events()
	for {
		if cErr := ctx.Err(); cErr != nil {
			return cErr
		}
		_, rErr := r.Next()
		if errors.Is(rErr, io.EOF) {
			return nil
		}
		// Other errors (decode failures, scanner I/O) are intentionally
		// ignored: the tap already captured the offending bytes, and
		// the only non-recoverable case — pipe closed — surfaces as EOF
		// on the next iteration.
	}
}

// writeKickoff prefixes the trace with a self-describing envelope so a
// downstream consumer of the JSONL stream can see exactly what prompt
// produced the engine output that follows. The leading underscore on
// the `type` keeps the envelope from colliding with any real engine
// event kind.
func writeKickoff(w io.Writer, prompt string) error {
	env := struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
	}{
		Type:   "_ralph_kickoff",
		Prompt: prompt,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
