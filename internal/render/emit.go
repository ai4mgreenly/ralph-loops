package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// Markers used by the activity log. Held as named constants so the
// success/error variants stay visually distinct and consistent across
// every emit path. Tool calls (assistant → operator) lead with
// [markerCall]; tool results (operator → assistant) lead with
// [markerResult] on success or [markerError] on failure. The terminal
// "result" event uses [markerSummary] / [markerSummaryError].
const (
	markerCall         = "←"
	markerResult       = "→"
	markerError        = "✗"
	markerSummary      = "←"
	markerSummaryError = "✗"
)

// Emitter renders one stream of claude events. It owns the
// per-iteration ledger of pending tool calls (so a tool_result block
// can be printed alongside the call that produced it) and the
// per-event timer that splits wall time into LLM / tool / other.
//
// One Emitter instance is reused across iterations: [Emitter.ResetIteration]
// clears the pending-tool ledger and re-anchors the timer at the
// start of each new iteration.
type Emitter struct {
	out          io.Writer
	rec          Recorder
	theme        *ui.Theme
	tools        map[string]toolRef
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

// WithVerbose toggles the rendering of low-signal events ("system",
// "rate_limit"). Defaults to false: those events are suppressed and
// only diagnostic kinds reach the operator.
func WithVerbose(v bool) EmitterOption {
	return func(e *Emitter) { e.verbose = v }
}

// WithOutputLines caps the number of tool-output lines replayed in
// the activity log per result. A value <= 0 leaves the built-in
// default ([defaultOutputLines]) in place.
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
// model name in so the operator sees what's actually being waited on
// (the engine is just a router; the model is what's billing). Has no
// effect when [WithSpinner] supplied a pre-built spinner.
func WithSpinnerLabel(label string) EmitterOption {
	return func(e *Emitter) { e.spinnerLabel = label }
}

// toolRef is the input to a not-yet-completed tool call, retained so
// the eventual tool_result can show the same parameter formatting as
// the original call.
type toolRef struct {
	name  string
	input json.RawMessage
}

// defaultOutputLines bounds how many lines of tool output we replay
// in the activity log when [Config.OutputLines] is unset. Bash, Read,
// and Edit output can be enormous; ten lines covers the common case
// (a build's tail end, the top of a file, a small edit hunk) without
// flooding the operator. Operators override via `--output-lines`.
const defaultOutputLines = 10

// NewEmitter constructs an Emitter writing to out and updating rec,
// using theme for colour and width decisions. The wall-clock source
// is taken indirectly through `time.Now` so tests can install a
// deterministic clock. opts override the documented defaults; see
// [WithVerbose], [WithOutputLines], and [WithSpinner].
func NewEmitter(out io.Writer, rec Recorder, theme *ui.Theme, opts ...EmitterOption) *Emitter {
	e := &Emitter{
		out:          out,
		rec:          rec,
		theme:        theme,
		tools:        make(map[string]toolRef),
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

// defaultSpinnerLabel is the spinner suffix when the caller does not
// override via [WithSpinnerLabel]. It matches the bare-default engine
// so a `ralph .` invocation with no flags reads naturally.
const defaultSpinnerLabel = "claude"

// Spinner returns the [ui.Spinner] the emitter brackets each event
// read with. Exposed so the loop driver can toggle the rotator on
// either side of a blocking stream read.
func (e *Emitter) Spinner() *ui.Spinner { return e.spinner }

// ResetIteration prepares the Emitter for a fresh iteration: drop any
// in-flight tool-call references (they cannot survive a new claude
// invocation) and re-anchor the per-event timer.
func (e *Emitter) ResetIteration() {
	clear(e.tools)
	e.lastAt = e.now()
}

// IterationBanner prints a rule-bracketed "iteration: N" header to
// mark the start of a new claude invocation. The rule spans the
// terminal width (or the panel fallback width when stdout is not a
// terminal).
func (e *Emitter) IterationBanner(n int) {
	rule := e.theme.Rule()
	fmt.Fprintln(e.out)
	fmt.Fprintln(e.out, rule)
	fmt.Fprintf(e.out, "iteration: %d\n", n)
	fmt.Fprintln(e.out, rule)
}

// OnAssistant handles the "assistant" event: indented plain text for
// model output, decorated lines for tool_use and thinking blocks.
// The wall-clock gap since the last event is attributed to LLM time.
func (e *Emitter) OnAssistant(a stream.Assistant) {
	now := e.now()
	e.rec.AddLLMTime(now.Sub(e.lastAt))
	e.lastAt = now

	blocks := a.Message.Content
	if len(blocks) == 0 {
		e.theme.Decorate(e.out, markerCall, "assistant (empty)")
		return
	}

	for _, b := range blocks {
		e.rec.TallyBlock(b.Type)
		switch b.Type {
		case stream.BlockText:
			e.theme.Lead(e.out, markerCall, e.capLines(strings.TrimRight(b.Text, " \t\r\n")))
		case stream.BlockToolUse:
			e.tools[b.ID] = toolRef{name: b.Name, input: b.Input}
			e.emitToolCall(b)
		case stream.BlockThinking:
			// Skip the noise from "… thinking (0 chars)" preludes that
			// claude emits before some tool calls; they carry no signal.
			if n := len(b.Thinking); n > 0 {
				e.theme.Decorate(e.out, markerCall, fmt.Sprintf("thinking (%d chars)", n))
			}
		default:
			e.theme.Decorate(e.out, markerCall, "assistant ["+b.Type+"]")
		}
	}
}

// OnUser handles the "user" event: typically a tool_result block
// matched against the call recorded in [Emitter.tools], or a replayed
// kickoff text. The wall-clock gap since the last event is attributed
// to tool time.
func (e *Emitter) OnUser(u stream.User) {
	now := e.now()
	e.rec.AddToolTime(now.Sub(e.lastAt))
	e.lastAt = now

	blocks := u.Message.Content
	if len(blocks) == 0 {
		e.theme.Decorate(e.out, markerResult, "user (empty)")
		return
	}

	for _, b := range blocks {
		e.rec.TallyBlock(b.Type)
		switch b.Type {
		case stream.BlockToolResult:
			e.emitToolResult(b, u.ToolUseResult)
		case stream.BlockText:
			e.emitUserText(b.Text)
		default:
			e.theme.Decorate(e.out, markerResult, "user ["+b.Type+"]")
		}
	}
}

func (e *Emitter) emitToolResult(b stream.Block, structured json.RawMessage) {
	ref, ok := e.tools[b.ToolUseID]
	delete(e.tools, b.ToolUseID)
	if !ok {
		ref = toolRef{name: "unknown"}
	}

	switch ref.name {
	case stream.ToolBash:
		e.emitBashResult(b, structured)
		return
	case stream.ToolRead:
		e.emitReadResult(b, ref)
		return
	case stream.ToolEdit:
		e.emitEditResult(b, ref)
		return
	case stream.ToolWrite:
		e.emitWriteResult(b, ref)
		return
	}

	status := "ok"
	if b.IsError {
		status = "ERR"
	}

	parts := []string{ref.name}
	if param := formatToolCallParam(ref.name, ref.input); param != "" {
		parts = append(parts, param)
	}
	parts = append(parts, status)
	if summary := formatToolResult(ref.name, b, structured); summary != "" {
		parts = append(parts, summary)
	}
	marker := markerResult
	if b.IsError {
		marker = markerError
	}
	e.theme.Decorate(e.out, marker, strings.Join(parts, "  "))
}

// emitToolCall renders one assistant tool_use block. Every tool call
// is followed by a blank separator so the `→` response sits one line
// below its `←` call — the visual gap, plus the subdued background
// fill on the call line, makes call/response pairs easy to scan. All
// shapes flow through [ui.Tool] so the gutter and soft-wrap behaviour
// stay consistent across every tool-call line in the UI.
func (e *Emitter) emitToolCall(b stream.Block) {
	switch b.Name {
	case stream.ToolBash:
		e.theme.Tool(e.out, markerCall, e.capLines(bashCommand(b.Input)), true)
		return
	case stream.ToolRead:
		e.theme.Tool(e.out, markerCall, "Read  "+readTarget(b.Input), true)
		return
	case stream.ToolEdit, stream.ToolWrite:
		e.theme.Tool(e.out, markerCall, b.Name+"  "+filePathOf(b.Input), true)
		return
	}
	content := b.Name
	if param := formatToolCallParam(b.Name, b.Input); param != "" {
		content += "  " + param
	}
	e.theme.Tool(e.out, markerCall, content, true)
}

// capLines truncates s to at most [Emitter.outputLines] lines (falling
// back to [defaultOutputLines] when unset), appending a `...` marker
// on truncation. Single-line and short multi-line bodies pass through
// unchanged. The cap exists to keep call-side decorators —
// [theme.Tool] for Bash heredocs, [theme.Lead] for assistant prose —
// from leaking unbounded payloads into the operator log; result-side
// blocks already get the same treatment via [Emitter.emitOutputBlock].
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
// by Bash, Read, Edit, Write, and any future tool that prefers raw
// output to a one-line summary. The first input line gets marker
// padded to [ui.Gutter] columns; later lines and soft-wrap
// continuations get [ui.Gutter] spaces, giving the whole block one
// clean left edge. Per-line colour is applied after wrapping so ANSI
// escapes do not count toward the wrap budget. When more than
// [Emitter.OutputLines] input lines are supplied the overflow is
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
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	if truncated {
		lines = append(lines, ui.Line{Text: "..."})
	}
	e.theme.WriteBlock(e.out, marker, lines, true)
}

// filePathOf pulls the `file_path` field from a tool_use input.
// Shared by the Read, Edit, and Write renderers (and any other tool
// that keys on a single path). Same fail-soft semantics as
// [bashCommand].
func filePathOf(input json.RawMessage) string {
	var s struct {
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(input, &s)
	return s.FilePath
}

// emitUserText renders a replayed user-text block (typically the
// iteration's kickoff prompt) as a tucked-under output block — same
// shape a Read tool response uses. Content is split on `\n`, capped
// at [Emitter.OutputLines] visible lines with a `...` truncation marker
// when the body runs longer, and shares the same gutter/wrap rules
// as every other block.
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

// OnResult handles the iteration's terminal "result" event: it
// records token usage and prints the result line with status, turn
// count, claude's own duration estimate and self-reported cost.
func (e *Emitter) OnResult(r stream.Result) {
	e.rec.TrackUsage(r.Usage)

	var bits []string
	if status := DecodeStatus(r.StructuredOutput); status != "" {
		bits = append(bits, "status="+status)
	}
	if r.NumTurns > 0 {
		bits = append(bits, fmt.Sprintf("turns=%d", r.NumTurns))
	}
	if r.DurationMS > 0 {
		bits = append(bits, "duration="+ui.FormatMilliseconds(r.DurationMS))
	}
	if r.TotalCostUSD > 0 {
		bits = append(bits, fmt.Sprintf("cost=$%.4f", r.TotalCostUSD))
	}

	marker := markerSummary
	if r.IsError {
		marker = markerSummaryError
	}
	content := "result"
	if len(bits) > 0 {
		content += "  " + strings.Join(bits, "  ")
	}
	e.theme.Decorate(e.out, marker, content)
}

// OnDecodeError surfaces a [stream.DecodeError] through the emitter's
// configured writer so the operator sees the offending raw line in the
// same activity log as every other event. The line is rendered with
// %q to escape non-printable or non-UTF-8 bytes, since malformed event
// lines may contain arbitrary garbage from the wire.
func (e *Emitter) OnDecodeError(de stream.DecodeError) {
	e.theme.Decorate(e.out, markerError, fmt.Sprintf("decode error: line %d: %q", de.Line, de.Bytes))
}

// OnSystem handles "system" events: session boot, model selection,
// permission mode, tool list, etc.
func (e *Emitter) OnSystem(s stream.System) {
	if !e.verbose {
		return
	}
	subtype := s.Subtype
	if subtype == "" {
		subtype = "system"
	}

	var bits []string
	if s.Model != "" {
		bits = append(bits, "model="+s.Model)
	}
	if s.Cwd != "" {
		bits = append(bits, "cwd="+s.Cwd)
	}
	if s.PermissionMode != "" {
		bits = append(bits, "perm="+s.PermissionMode)
	}
	if n := len(s.Tools); n > 0 {
		bits = append(bits, fmt.Sprintf("tools=%d", n))
	}

	content := subtype
	if len(bits) > 0 {
		content += "  " + strings.Join(bits, "  ")
	}
	e.theme.Decorate(e.out, markerCall, content)
}

// OnRateLimit reports the rate-limit envelope claude attaches to some
// events. The fields surfaced match the Ruby driver's set.
func (e *Emitter) OnRateLimit(r stream.RateLimit) {
	if !e.verbose {
		return
	}
	info := r.Info
	if info == nil {
		info = &stream.RateLimitInfo{}
	}

	var bits []string
	if info.RateLimitType != "" {
		bits = append(bits, "type="+info.RateLimitType)
	}
	if info.Status != "" {
		bits = append(bits, "status="+info.Status)
	}
	if info.Utilization != 0 {
		bits = append(bits, fmt.Sprintf("util=%.1f%%", info.Utilization*100))
	}
	if info.ResetsAt != 0 {
		bits = append(bits, "resets="+time.Unix(info.ResetsAt, 0).UTC().Format(time.RFC3339))
	}
	if info.IsUsingOverage {
		bits = append(bits, "overage")
	}

	content := "rate_limit"
	if len(bits) > 0 {
		content += "  " + strings.Join(bits, "  ")
	}
	e.theme.Decorate(e.out, markerCall, content)
}

// DecodeStatus extracts the schema-constrained status field from a
// raw structured-output payload. Returns an empty string for any
// shape that doesn't match the expected envelope.
func DecodeStatus(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var so stream.StatusOutput
	if err := json.Unmarshal(raw, &so); err != nil {
		return ""
	}
	return so.Status
}
