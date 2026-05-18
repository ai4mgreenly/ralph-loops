package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// Markers used by the activity log. Held as named constants so the
// success/error variants stay visually distinct and consistent across
// every emit path. Assistant prose and tool-call headers lead with
// [markerCall]; tool results lead with [markerResult] on success or
// [markerError] on failure.
const (
	markerCall   = "←"
	markerResult = "→"
	markerError  = "✗"
)

// Emitter renders one stream of pi settled events. It owns the
// per-iteration ledger of in-flight tool calls (so a
// tool_execution_end can be timed against its tool_execution_start and
// the `edit` diff reconstructed from the start's args) and the
// per-event timer that splits wall time into LLM / tool work.
//
// One Emitter instance is reused across iterations:
// [Emitter.ResetIteration] clears the pending-tool ledger and
// re-anchors the timer at the start of each new pi invocation.
type Emitter struct {
	out          io.Writer
	rec          Recorder
	theme        *ui.Theme
	tools        map[string]pendingTool
	lastAt       time.Time
	now          func() time.Time
	verbose      bool
	spinner      *ui.Spinner
	spinnerLabel string
	outputLines  int
}

// EmitterOption configures one knob on an [Emitter] at construction
// time. Pass options to [NewEmitter] rather than mutating exported
// fields after the fact.
type EmitterOption func(*Emitter)

// WithVerbose toggles the rendering of low-signal events (the
// [stream.Session] banner and the known-but-unused carriers). Defaults
// to false: those are tallied but not painted, so only assistant
// prose, tool activity, and the terminal summary reach the operator.
func WithVerbose(v bool) EmitterOption {
	return func(e *Emitter) { e.verbose = v }
}

// WithOutputLines caps the number of tool-output lines replayed in the
// activity log per result. A value <= 0 leaves the built-in default
// ([defaultOutputLines]) in place.
func WithOutputLines(n int) EmitterOption {
	return func(e *Emitter) {
		if n > 0 {
			e.outputLines = n
		}
	}
}

// WithSpinner overrides the [ui.Spinner] the emitter constructs by
// default. Tests pass a writer-only spinner here; production code
// usually relies on the default.
func WithSpinner(s *ui.Spinner) EmitterOption {
	return func(e *Emitter) { e.spinner = s }
}

// WithSpinnerLabel customises the "waiting for X" prefix the default
// spinner paints between event reads. Production callers thread the
// model name in so the operator sees what is actually being waited on
// (the engine is just a router; the model is what's billing). Has no
// effect when [WithSpinner] supplied a pre-built spinner.
func WithSpinnerLabel(label string) EmitterOption {
	return func(e *Emitter) { e.spinnerLabel = label }
}

// pendingTool is the bookkeeping retained between a
// [stream.ToolExecutionStart] and its matching
// [stream.ToolExecutionEnd]: the start time (so the execution span can
// be attributed to tool wall-time) and the raw start args (so the
// `edit` tool's diff can be reconstructed when the end arrives).
type pendingTool struct {
	startedAt time.Time
	args      []byte
}

// defaultOutputLines bounds how many lines of tool output we replay in
// the activity log when [WithOutputLines] is unset. A read of a large
// file or a noisy command can be enormous; ten lines covers the common
// case without flooding the operator. Operators override via
// `--output-lines`.
const defaultOutputLines = 10

// defaultSpinnerLabel is the spinner suffix when the caller does not
// override via [WithSpinnerLabel]. It names the engine (pi) so a bare
// `ralph .` invocation with no flags reads naturally.
const defaultSpinnerLabel = "pi"

// NewEmitter constructs an Emitter writing to out and updating rec,
// using theme for colour and width decisions. The wall-clock source is
// taken indirectly through `time.Now` so tests can install a
// deterministic clock. opts override the documented defaults; see
// [WithVerbose], [WithOutputLines], [WithSpinner], and
// [WithSpinnerLabel].
func NewEmitter(out io.Writer, rec Recorder, theme *ui.Theme, opts ...EmitterOption) *Emitter {
	e := &Emitter{
		out:          out,
		rec:          rec,
		theme:        theme,
		tools:        make(map[string]pendingTool),
		now:          time.Now,
		spinnerLabel: defaultSpinnerLabel,
		outputLines:  defaultOutputLines,
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.spinner == nil {
		e.spinner = ui.NewSpinner(out, "waiting for "+e.spinnerLabel, theme.UseColor())
	}
	return e
}

// Spinner returns the [ui.Spinner] the emitter brackets each event
// read with. Exposed so the loop driver can toggle the rotator on
// either side of a blocking stream read.
func (e *Emitter) Spinner() *ui.Spinner { return e.spinner }

// ResetIteration prepares the Emitter for a fresh iteration: drop any
// in-flight tool-call references (they cannot survive a new pi
// invocation) and re-anchor the per-event timer.
func (e *Emitter) ResetIteration() {
	clear(e.tools)
	e.lastAt = e.now()
}

// IterationBanner prints a rule-bracketed "iteration: N" header to
// mark the start of a new pi invocation. The rule spans the terminal
// width (or the panel fallback width when stdout is not a terminal).
func (e *Emitter) IterationBanner(n int) {
	rule := e.theme.Rule()
	fmt.Fprintln(e.out, rule)
	fmt.Fprintf(e.out, "iteration: %d\n", n)
	fmt.Fprintln(e.out, rule)
}

// OnEvent is the single dispatch entry point: it tallies the event by
// kind (so stats count every pi event type, including the
// known-but-unused carriers) and routes the rich types to their
// renderer. Unrendered kinds — the known-but-unused carriers, an
// [stream.UnknownEvent], a [stream.TurnEnd] — are tallied and dropped:
// pi's authoritative per-iteration accounting comes from
// [stream.AgentEnd], which the loop reads directly.
//
// Callers pass every [stream.Event] from [stream.Reader.Next] here,
// including the [stream.UnknownEvent] returned alongside a decode
// error.
func (e *Emitter) OnEvent(ev stream.Event) {
	if ev == nil {
		return
	}
	e.rec.TallyEvent(ev.Kind())
	switch v := ev.(type) {
	case stream.Session:
		e.onSession(v)
	case stream.MessageEnd:
		e.onMessageEnd(v)
	case stream.ToolExecutionStart:
		e.onToolStart(v)
	case stream.ToolExecutionEnd:
		e.onToolEnd(v)
	}
}

// onSession renders pi's opening [stream.Session] banner — protocol
// version, session id, cwd — but only in verbose mode: it carries no
// build signal, just provenance for a saved log.
func (e *Emitter) onSession(s stream.Session) {
	if !e.verbose {
		return
	}
	var bits []string
	if s.Version != 0 {
		bits = append(bits, fmt.Sprintf("version=%d", s.Version))
	}
	if s.ID != "" {
		bits = append(bits, "id="+s.ID)
	}
	if s.Cwd != "" {
		bits = append(bits, "cwd="+s.Cwd)
	}
	content := "session"
	if len(bits) > 0 {
		content += "  " + strings.Join(bits, "  ")
	}
	e.theme.Decorate(e.out, markerCall, content)
}

// onMessageEnd renders one settled message. Assistant messages get
// their text/thinking blocks painted and their usage captured for the
// partial fallback; toolCall blocks are tallied but not rendered here
// (the tool channel is the [stream.ToolExecutionStart]/End pair, per
// the locked de-dupe decision). The "toolResult" role is dropped
// entirely — pi emits a redundant toolResult message_end alongside
// every tool_execution_end and rendering both would double-print. User
// messages (the kickoff replay) are rendered as a tucked-under block.
//
// The wall-clock gap since the previous event is attributed to LLM
// time when an assistant message settles.
func (e *Emitter) onMessageEnd(m stream.MessageEnd) {
	msg := m.Message
	switch msg.Role {
	case stream.RoleToolResult:
		// Redundant with the tool_execution_* channel; drop without
		// rendering or counting (the de-dupe decision).
		return
	case stream.RoleAssistant:
		now := e.now()
		e.rec.AddLLMTime(now.Sub(e.lastAt))
		e.lastAt = now
		e.rec.TrackMessageUsage(msg.Usage, msg.Provider, effectiveModel(msg), msg.StopReason)
		e.renderAssistantBlocks(msg.Content)
	case stream.RoleUser:
		e.renderUserBlocks(msg.Content)
	default:
		e.renderUserBlocks(msg.Content)
	}
}

// effectiveModel is the Q6 "effective model": the model the provider
// actually served the request with (ResponseModel) when pi reports it,
// otherwise the requested Model.
func effectiveModel(m stream.PiMessage) string {
	if m.ResponseModel != "" {
		return m.ResponseModel
	}
	return m.Model
}

// renderAssistantBlocks paints an assistant message's content: prose
// in a lead block, a thinking marker (its body is encrypted noise pi
// cannot replay, so only the presence is signalled), and a tally for
// toolCall blocks (whose rendering is the tool_execution channel's
// job). Each block type is counted on the recorder.
func (e *Emitter) renderAssistantBlocks(blocks []stream.ContentBlock) {
	if len(blocks) == 0 {
		e.theme.Decorate(e.out, markerCall, "assistant (empty)")
		return
	}
	for _, b := range blocks {
		e.rec.TallyBlock(b.Type)
		switch b.Type {
		case stream.BlockText:
			e.theme.Lead(e.out, markerCall, e.capLines(strings.TrimRight(b.Text, " \t\r\n")))
		case stream.BlockThinking:
			if n := len(b.Thinking); n > 0 {
				e.theme.Decorate(e.out, markerCall, fmt.Sprintf("thinking (%d chars)", n))
			}
		case stream.BlockToolCall:
			// The tool channel is tool_execution_start/end; the toolCall
			// block here would double-render. Tally only.
		default:
			e.theme.Decorate(e.out, markerCall, "assistant ["+b.Type+"]")
		}
	}
}

// renderUserBlocks paints a settled user message — in practice the
// iteration's kickoff prompt replayed back — as a tucked-under output
// block so it reads like any other result body.
func (e *Emitter) renderUserBlocks(blocks []stream.ContentBlock) {
	for _, b := range blocks {
		if b.Type == stream.BlockText {
			e.emitUserText(b.Text)
		}
	}
}

// onToolStart records the tool's start (for timing and, for `edit`,
// for diff reconstruction at end) and prints the B-lite header:
// toolName plus its primary argument (the first present of
// path/command/pattern in the decoded args object).
func (e *Emitter) onToolStart(s stream.ToolExecutionStart) {
	e.tools[s.ToolCallID] = pendingTool{
		startedAt: e.now(),
		args:      append([]byte(nil), s.Args...),
	}

	header := s.ToolName
	if param := primaryArg(s.Args); param != "" {
		header += "  " + param
	}
	e.theme.Tool(e.out, markerCall, e.capLines(header), true)
}

// onToolEnd closes a tool execution: attribute the start→end span to
// tool wall-time, record the outcome for the tool/error counts, then
// render the result. For `edit` the locked decision is to reconstruct
// a colorized diff from the start args' edits[] through the existing
// diff+highlight code; every other tool prints its result.content[]
// text, error-styled when isError.
func (e *Emitter) onToolEnd(end stream.ToolExecutionEnd) {
	ref, ok := e.tools[end.ToolCallID]
	delete(e.tools, end.ToolCallID)
	if ok {
		e.rec.AddToolTime(e.now().Sub(ref.startedAt))
	}
	e.rec.TrackToolOutcome(end.ToolName, end.IsError)

	marker := markerResult
	if end.IsError {
		marker = markerError
	}

	if end.ToolName == toolEdit {
		e.emitEditDiff(marker, ref.args)
		return
	}

	text := strings.TrimRight(resultContentText(end.Result), "\n")
	if text == "" {
		// A bare marker line carries no signal; an error with no body
		// still earns a marker so the failure is visible.
		if end.IsError {
			e.theme.Decorate(e.out, marker, end.ToolName+" error")
		}
		return
	}
	lines := make([]ui.Line, 0, strings.Count(text, "\n")+1)
	color := ui.Plain
	if end.IsError {
		color = ui.DimRed
	}
	for _, l := range strings.Split(text, "\n") {
		lines = append(lines, ui.Line{Text: strings.TrimRight(l, " \t\r"), Color: color})
	}
	e.emitOutputBlock(marker, lines)
}

// OnDecodeError surfaces a [stream.DecodeError] through the emitter's
// configured writer so the operator sees the offending raw line in the
// same activity log as every other event. The line is rendered with %q
// to escape non-printable or non-UTF-8 bytes, since malformed event
// lines may contain arbitrary garbage from the wire.
func (e *Emitter) OnDecodeError(de stream.DecodeError) {
	e.theme.Decorate(e.out, markerError, fmt.Sprintf("decode error: line %d: %q", de.Line, de.Bytes))
}

// capLines truncates s to at most [Emitter.outputLines] lines (falling
// back to [defaultOutputLines] when unset), appending a `...` marker on
// truncation. Single-line and short multi-line bodies pass through
// unchanged. The cap keeps call-side decorators — [ui.Theme.Tool] for
// tool headers, [ui.Theme.Lead] for assistant prose — from leaking
// unbounded payloads into the operator log; result-side blocks already
// get the same treatment via [Emitter.emitOutputBlock].
func (e *Emitter) capLines(s string) string {
	maxLines := e.outputLines
	if maxLines <= 0 {
		maxLines = defaultOutputLines
	}
	parts := strings.Split(s, "\n")
	if len(parts) <= maxLines {
		return s
	}
	capped := make([]string, 0, maxLines+1)
	capped = append(capped, parts[:maxLines]...)
	capped = append(capped, "...")
	return strings.Join(capped, "\n")
}

// emitOutputBlock renders the shared terminal-style result block used
// by the generic tool renderer and the edit diff. The first input line
// gets its marker padded to [ui.Gutter] columns; later lines and
// soft-wrap continuations get [ui.Gutter] spaces, giving the whole
// block one clean left edge. Per-line colour is applied after wrapping
// so ANSI escapes do not count toward the wrap budget. When more than
// [Emitter.outputLines] input lines are supplied the overflow is
// dropped and a `...` line is appended. An empty lines slice emits
// nothing — a bare marker line carries no signal for the operator.
func (e *Emitter) emitOutputBlock(marker string, lines []ui.Line) {
	if len(lines) == 0 {
		return
	}
	maxLines := e.outputLines
	if maxLines <= 0 {
		maxLines = defaultOutputLines
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, ui.Line{Text: "..."})
	}
	e.theme.WriteBlock(e.out, marker, lines, true)
}

// emitUserText renders a replayed user-text block (typically the
// iteration's kickoff prompt) as a tucked-under output block. Content
// is split on `\n`, capped at [Emitter.outputLines] visible lines with
// a `...` truncation marker when the body runs longer, and shares the
// same gutter/wrap rules as every other block.
func (e *Emitter) emitUserText(text string) {
	body := strings.TrimRight(text, "\n")
	var lines []ui.Line
	if body != "" {
		for _, l := range strings.Split(body, "\n") {
			lines = append(lines, ui.Line{Text: strings.TrimRight(l, " \t\r"), Color: ui.Orange})
		}
	}
	e.emitOutputBlock(markerResult, lines)
}
