package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// runRaw executes the debug passthrough enabled by [WithRaw]. It spawns
// one one-shot pi session (the kickoff prompt is already baked into
// pi's argv via [agent.Config.Prompt]), writes a self-describing
// kickoff envelope onto w so the trace records its own input, then
// drains pi's event stream to EOF while the spawner's stdout tap
// mirrors raw bytes onto w. No rendering, stats, status decode, or
// results.jsonl happens here — the goal is a verbatim pi wire trace.
//
// Per Q9 the iteration outcome is event-driven, so raw mode no longer
// tolerates a 0/1 exit specially: it simply drains to EOF and lets
// Close surface whatever it surfaces. A non-zero exit (always advisory
// — see [agent.ExitError]) is not propagated as a run error here
// because raw mode has no parsed outcome to gate on; the verbatim trace
// is the deliverable, and the operator reads it directly.
func runRaw(ctx context.Context, cfg Config, o options, w io.Writer, sp Spawner) error {
	sess, err := sp.Spawn(ctx, agentConfig(cfg, o))
	if err != nil {
		return err
	}

	// Defer Close so a panic during the drain doesn't leak the child.
	// The Close result is intentionally discarded: under the
	// event-driven outcome model (Q9) pi's exit code is advisory only,
	// and raw mode parses nothing it could gate that judgment on.
	defer func() { _ = sess.Close() }()

	if kErr := writeKickoff(w, cfg.Prompt); kErr != nil {
		return fmt.Errorf("write kickoff: %w", kErr)
	}

	// Drain events to EOF. The reader's bytes flow through the spawner's
	// stdout tap, so the parsed events themselves are discarded; decode
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
// produced the pi output that follows. The leading underscore on the
// `type` keeps the envelope from colliding with any real pi event kind.
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
