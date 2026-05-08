// Package render owns the per-event rendering layer of the ralph
// driver: the [Emitter] that pretty-prints the claude stream-json
// event flow, plus the tool-specific formatters, syntax highlighter,
// and line-by-line differ used by the Read/Edit/Write/Bash result
// renderers.
//
// The package is split across several files:
//
//   - render.go    Package doc, the [Recorder] seam, the iteration rule.
//   - emit.go      Per-event-type pretty printing and timing accounting.
//   - format.go    Tool-specific parameter and result formatters.
//   - diff.go      Line-by-line LCS diff used by [Emitter.emitEditResult].
//   - highlight.go Chroma-driven syntax highlighting used by Read/Edit/Write.
//
// The package depends on `internal/stream` for the typed event model
// and `internal/ui` for output primitives, but knows nothing about the
// outer iteration loop. The producer (the loop's stats accumulator)
// supplies a [Recorder] so render can attribute timing and tally
// blocks/usage without knowing about the concrete `*stats` type.
package render

import (
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// Recorder accumulates per-event accounting that the [Emitter]
// produces while rendering the claude stream-json flow. The interface
// lives in the consumer package (this one) and is implemented by the
// loop's stats type — the canonical "accept interfaces, return
// structs" inversion.
//
// The surface is intentionally minimal: exactly the methods emit.go
// calls on the recorder, no more.
type Recorder interface {
	// TallyBlock counts one assistant/user content block of the given
	// type (text, tool_use, tool_result, ...).
	TallyBlock(t string)

	// AddLLMTime attributes d to model think/generate time. Called
	// when an assistant event is dispatched.
	AddLLMTime(d time.Duration)

	// AddToolTime attributes d to tool-execution time. Called when a
	// user event (typically a tool_result) is dispatched.
	AddToolTime(d time.Duration)

	// TrackUsage rolls a single result event's token usage into the
	// running totals. A nil pointer is tolerated and ignored.
	TrackUsage(u *stream.Usage)
}
