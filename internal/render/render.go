// Package render owns the per-event rendering layer of the ralph
// driver: the [Emitter] that pretty-prints pi's settled-event flow,
// plus the single generic tool renderer, syntax highlighter, and
// line-by-line differ used to reconstruct the `edit` tool's diff.
//
// The package is split across several files:
//
//   - render.go     Package doc and the [Recorder] seam.
//   - emit.go       The [Emitter] type, options, event dispatch, and the
//     single generic tool renderer.
//   - emit_edit.go  The `edit`-tool diff helper (engine-agnostic).
//   - format.go     pi argument/result extraction helpers.
//   - diff.go       Line-by-line LCS diff used by the edit-diff helper.
//   - highlight.go  Chroma-driven syntax highlighting for the edit diff.
//
// The package depends on `internal/stream` for the typed pi event
// model and `internal/ui` for output primitives, but knows nothing
// about the outer iteration loop. The [Recorder] interface lives on
// the consumer side (here): the loop's stats accumulator implements it
// and is passed in at [NewEmitter] time, so render can attribute
// timing and tally events/blocks/usage without ever importing the
// loop's concrete stats type. This is the canonical "accept interfaces
// at the boundary" inversion — the producer of the data defines what
// it can deliver, and the consumer narrows that to what it actually
// needs.
package render

import (
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// Recorder accumulates the per-event accounting the [Emitter] observes
// while rendering pi's settled-event flow. The interface lives in the
// consumer package (this one) and is implemented by the loop's stats
// type — the canonical "accept interfaces, return structs" inversion.
//
// The surface is intentionally minimal and cohesive: exactly the hooks
// emit.go drives, shaped to pi's model. The authoritative per-iteration
// token/cost tally is computed by the loop from the terminal
// [stream.AgentEnd] (NOT through this interface); these hooks feed live
// scrollback accounting, the process-died-early partial fallback, and
// the tool/turn counts.
//
// Method contract (the loop slice implements this verbatim):
//
//   - [Recorder.TallyEvent] is called once for every decoded event,
//     keyed by [stream.Event.Kind] (so stats can count events of every
//     pi type, including the known-but-unused carriers).
//   - [Recorder.TallyBlock] is called once per assistant content block,
//     keyed by the block Type (text/thinking/toolCall).
//   - [Recorder.AddLLMTime] attributes ralph's own wall clock between
//     the previous event and an assistant [stream.MessageEnd] to model
//     think/generate time.
//   - [Recorder.AddToolTime] attributes ralph's own wall clock spanning
//     a tool execution (start → end) to tool time.
//   - [Recorder.TrackMessageUsage] captures one assistant message's
//     usage for the partial fallback, paired with the provider, the
//     effective model (responseModel if non-empty, else model), and the
//     stop reason. A nil usage pointer is tolerated and ignored.
//   - [Recorder.TrackToolOutcome] reports one completed tool execution
//     so stats can count tool calls and tool errors.
type Recorder interface {
	// TallyEvent counts one decoded event of the given wire-format
	// kind (see [stream.Event.Kind]).
	TallyEvent(kind string)

	// TallyBlock counts one assistant content block of the given type
	// (see the stream.Block* constants: text, thinking, toolCall).
	TallyBlock(blockType string)

	// AddLLMTime attributes d to model think/generate time. Called when
	// an assistant message settles.
	AddLLMTime(d time.Duration)

	// AddToolTime attributes d to tool-execution time. Called when a
	// tool execution completes, covering the start→end span.
	AddToolTime(d time.Duration)

	// TrackMessageUsage captures one assistant message's per-turn usage
	// (the partial fallback for a process that dies before agent_end),
	// together with the message's provider, effective model
	// (responseModel if non-empty else model), and stop reason. A nil
	// usage is tolerated and ignored.
	TrackMessageUsage(u *stream.Usage, provider, model, stopReason string)

	// TrackToolOutcome records one completed tool execution so stats
	// can count tool calls and tool errors. isError reports whether the
	// tool itself signalled failure.
	TrackToolOutcome(toolName string, isError bool)
}
