package loop

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// emitter renders one stream of claude events. It owns the
// per-iteration ledger of pending tool calls (so a tool_result block
// can be printed alongside the call that produced it) and the
// per-event timer that splits wall time into LLM / tool / other.
//
// One emitter instance is reused across iterations: [resetIteration]
// clears the pending-tool ledger and re-anchors the timer at the
// start of each new iteration.
type emitter struct {
	out     io.Writer
	stats   *stats
	tools   map[string]toolRef
	lastAt  time.Time
	now     func() time.Time
	verbose bool
	spinner *ui.Spinner
}

// toolRef is the input to a not-yet-completed tool call, retained so
// the eventual tool_result can show the same parameter formatting as
// the original call.
type toolRef struct {
	name  string
	input json.RawMessage
}

// newEmitter constructs an emitter writing to out and updating s.
// The wall-clock source is taken indirectly through `time.Now` so
// tests can install a deterministic clock.
func newEmitter(out io.Writer, s *stats) *emitter {
	return &emitter{
		out:     out,
		stats:   s,
		tools:   make(map[string]toolRef),
		now:     time.Now,
		spinner: ui.NewSpinner(out, "waiting for claude"),
	}
}

// resetIteration prepares the emitter for a fresh iteration: drop any
// in-flight tool-call references (they cannot survive a new claude
// invocation) and re-anchor the per-event timer.
func (e *emitter) resetIteration() {
	clear(e.tools)
	e.lastAt = e.now()
}

// iterationBanner prints a rule-bracketed "iteration: N" header to
// mark the start of a new claude invocation. The rule spans the
// terminal width (or the panel fallback width when stdout is not a
// terminal).
func (e *emitter) iterationBanner(n int) {
	rule := buildRule()
	fmt.Fprintln(e.out)
	fmt.Fprintln(e.out, rule)
	fmt.Fprintf(e.out, "iteration: %d\n", n)
	fmt.Fprintln(e.out, rule)
}

// onAssistant handles the "assistant" event: indented plain text for
// model output, decorated lines for tool_use and thinking blocks.
// The wall-clock gap since the last event is attributed to LLM time.
func (e *emitter) onAssistant(a stream.Assistant) {
	now := e.now()
	e.stats.addLLMTime(now.Sub(e.lastAt))
	e.lastAt = now

	blocks := a.Message.Content
	if len(blocks) == 0 {
		ui.Decorate(e.out, "←", "assistant (empty)") // was: ↑
		return
	}

	for _, b := range blocks {
		e.stats.tallyBlock(b.Type)
		switch b.Type {
		case stream.BlockText:
			ui.Lead(e.out, "←", strings.TrimRight(b.Text, " \t\r\n")) // was: *
		case stream.BlockToolUse:
			e.tools[b.ID] = toolRef{name: b.Name, input: b.Input}
			e.emitToolCall(b)
		case stream.BlockThinking:
			// Skip the noise from "… thinking (0 chars)" preludes that
			// claude emits before some tool calls; they carry no signal.
			if n := len(b.Thinking); n > 0 {
				ui.Decorate(e.out, "←", fmt.Sprintf("thinking (%d chars)", n)) // was: …
			}
		default:
			ui.Decorate(e.out, "←", "assistant ["+b.Type+"]") // was: ↑
		}
	}
}

// onUser handles the "user" event: typically a tool_result block
// matched against the call recorded in [emitter.tools], or a replayed
// kickoff text. The wall-clock gap since the last event is attributed
// to tool time.
func (e *emitter) onUser(u stream.User) {
	now := e.now()
	e.stats.addToolTime(now.Sub(e.lastAt))
	e.lastAt = now

	blocks := u.Message.Content
	if len(blocks) == 0 {
		ui.Decorate(e.out, "→", "user (empty)")
		return
	}

	for _, b := range blocks {
		e.stats.tallyBlock(b.Type)
		switch b.Type {
		case stream.BlockToolResult:
			e.emitToolResult(b, u.ToolUseResult)
		case stream.BlockText:
			e.emitUserText(b.Text)
		default:
			ui.Decorate(e.out, "→", "user ["+b.Type+"]")
		}
	}
}

func (e *emitter) emitToolResult(b stream.Block, structured json.RawMessage) {
	ref, ok := e.tools[b.ToolUseID]
	delete(e.tools, b.ToolUseID)
	if !ok {
		ref = toolRef{name: "unknown"}
	}

	switch ref.name {
	case "Bash":
		e.emitBashResult(b, structured)
		return
	case "Read":
		e.emitReadResult(b, ref)
		return
	case "Edit":
		e.emitEditResult(b, ref)
		return
	case "Write":
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
	ui.Decorate(e.out, "→", strings.Join(parts, "  "))
}

// emitToolCall renders one assistant tool_use block. Every tool call
// is followed by a blank separator so the `→` response sits one line
// below its `←` call — the visual gap, plus the subdued background
// fill on the call line, makes call/response pairs easy to scan. All
// shapes flow through [ui.Tool] so the gutter and soft-wrap behaviour
// stay consistent across every tool-call line in the UI.
func (e *emitter) emitToolCall(b stream.Block) {
	switch b.Name {
	case "Bash":
		ui.Tool(e.out, "←", bashCommand(b.Input), true)
		return
	case "Read", "Edit", "Write":
		ui.Tool(e.out, "←", b.Name+"  "+filePathOf(b.Input), true)
		return
	}
	content := b.Name
	if param := formatToolCallParam(b.Name, b.Input); param != "" {
		content += "  " + param
	}
	ui.Tool(e.out, "←", content, true)
}

// outputLineCap bounds how many lines of tool output we replay in the
// activity log. Bash, Read, and Edit output can be enormous; ten
// lines covers the common case (a build's tail end, the top of a
// file, a small edit hunk) without flooding the operator.
const outputLineCap = 10

// emitOutputBlock renders the shared terminal-style result block used
// by Bash, Read, Edit, Write, and any future tool that prefers raw
// output to a one-line summary. The first input line gets marker
// padded to [ui.Gutter] columns; later lines and soft-wrap
// continuations get [ui.Gutter] spaces, giving the whole block one
// clean left edge. Per-line colour is applied after wrapping so ANSI
// escapes do not count toward the wrap budget. When more than
// [outputLineCap] input lines are supplied the overflow is dropped
// and a `...` line is appended.
func (e *emitter) emitOutputBlock(marker string, lines []ui.Line) {
	truncated := false
	if len(lines) > outputLineCap {
		lines = lines[:outputLineCap]
		truncated = true
	}
	if truncated {
		lines = append(lines, ui.Line{Text: "..."})
	}
	ui.WriteBlock(e.out, marker, lines, true)
}

// emitBashResult renders the result of a Bash tool call as a tight
// terminal-style block: stdout lines first, stderr lines after in dim
// red, capped at [outputLineCap] with a trailing `...` when more were
// available.
func (e *emitter) emitBashResult(b stream.Block, structured json.RawMessage) {
	var s struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if len(structured) > 0 {
		_ = json.Unmarshal(structured, &s)
	}

	marker := "→" // error variant was: ✗
	if b.IsError {
		marker = "→"
	}

	var lines []ui.Line
	push := func(text string, color ui.Color) {
		text = strings.TrimRight(text, "\n")
		if text == "" {
			return
		}
		for _, l := range strings.Split(text, "\n") {
			lines = append(lines, ui.Line{Text: strings.TrimRight(l, " \t\r"), Color: color})
		}
	}
	push(s.Stdout, ui.Plain)
	push(s.Stderr, ui.DimRed)

	e.emitOutputBlock(marker, lines)
}

// emitReadResult renders the result of a Read tool call as the same
// terminal-style block used by Bash, but populated from the file
// content claude returns in the result block. The result text comes
// back with `cat -n`-style line-number prefixes; those are stripped
// so what the operator sees matches what the agent saw, then the
// stripped body is run through the syntax highlighter using the
// original Read call's `file_path` to pick a lexer.
func (e *emitter) emitReadResult(b stream.Block, ref toolRef) {
	marker := "→" // error variant was: ✗
	if b.IsError {
		marker = "→"
	}

	content := strings.TrimRight(extractContentText(b), "\n")
	if content == "" {
		e.emitOutputBlock(marker, nil)
		return
	}

	stripped := make([]string, 0, strings.Count(content, "\n")+1)
	for _, l := range strings.Split(content, "\n") {
		stripped = append(stripped, strings.TrimRight(stripLineNumber(l), " \t\r"))
	}
	// Drop a single trailing empty element so the line count matches
	// what chroma's output produces after [splitLinesNoTrailing] strips
	// its own trailing newline. Without this, files whose last source
	// line is blank (or whose content ends in `\n` once stripped of line
	// numbers) trigger the length-mismatch fallback to plain text.
	if n := len(stripped); n > 0 && stripped[n-1] == "" {
		stripped = stripped[:n-1]
	}

	highlighted := highlightLines(filePathOf(ref.input), strings.Join(stripped, "\n"))
	if len(highlighted) != len(stripped) {
		highlighted = stripped
	}

	lines := make([]ui.Line, len(highlighted))
	for i, text := range highlighted {
		lines[i] = ui.Line{Text: text}
	}

	e.emitOutputBlock(marker, lines)
}

// emitEditResult renders the result of an Edit tool call as a
// line-by-line diff between the call's old_string and new_string:
// removed lines get a `- ` prefix and a dim-red background tint,
// additions get `+ ` and a dim-green background tint, and surrounding
// context (lines common to both) get two leading spaces and no tint.
// Each line body is syntax-highlighted using a lexer chosen from the
// original Edit call's `file_path`; the bg-restoration in
// [ui.Color.Paint] keeps the diff tint solid across chroma's
// per-token resets. The diff is computed from the saved tool-call
// input rather than the result block, so the diff reflects what the
// agent intended to change even when the edit failed.
func (e *emitter) emitEditResult(b stream.Block, ref toolRef) {
	marker := "→" // error variant was: ✗
	if b.IsError {
		marker = "→"
	}

	var input struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	_ = json.Unmarshal(ref.input, &input)

	filePath := filePathOf(ref.input)
	highlightedOld := indexHighlightedLines(filePath, input.OldString)
	highlightedNew := indexHighlightedLines(filePath, input.NewString)

	d := diffLines(input.OldString, input.NewString)
	lines := make([]ui.Line, 0, len(d))
	var oi, ni int
	for _, op := range d {
		var prefix string
		var color ui.Color
		var body string
		switch op.op {
		case diffRemove:
			prefix, color = "- ", ui.DiffRemoveBg
			body = pickHighlighted(highlightedOld, oi, op.text)
			oi++
		case diffAdd:
			prefix, color = "+ ", ui.DiffAddBg
			body = pickHighlighted(highlightedNew, ni, op.text)
			ni++
		default:
			prefix, color = "  ", ui.Plain
			body = pickHighlighted(highlightedNew, ni, op.text)
			oi++
			ni++
		}
		lines = append(lines, ui.Line{Text: prefix + body, Color: color})
	}

	e.emitOutputBlock(marker, lines)
}

// indexHighlightedLines returns the lines of content highlighted for
// filePath, or nil if highlighting failed in a way that left the line
// count out of sync with the source. Callers pair the returned slice
// with the original lines so a fall-through to plain text is always
// possible per line.
func indexHighlightedLines(filePath, content string) []string {
	if content == "" {
		return nil
	}
	plain := splitDiffLines(content)
	hi := highlightLines(filePath, content)
	if len(hi) != len(plain) {
		return plain
	}
	return hi
}

// pickHighlighted returns lines[i] when in range, otherwise fallback.
// Used by the Edit and Write renderers as a per-line fallback so a
// short or nil highlighted slice never panics the renderer — it just
// degrades to the raw source text for that line.
func pickHighlighted(lines []string, i int, fallback string) string {
	if i < 0 || i >= len(lines) {
		return fallback
	}
	return lines[i]
}

// emitWriteResult renders the result of a Write tool call as a
// "diff from nothing": every line of the new content is shown as an
// addition (`+ ` prefix, green background tint), mirroring the way
// Edit renders its hunks. Line bodies are syntax-highlighted from
// the original Write call's `file_path`. This is faithful for
// new-file writes (the common case) and a stylised view for
// overwrites — the `←  Write  <path>` header is the operator's cue
// that something pre-existing may have been replaced.
func (e *emitter) emitWriteResult(b stream.Block, ref toolRef) {
	marker := "→" // error variant was: ✗
	if b.IsError {
		marker = "→"
	}

	content := strings.TrimRight(writeContent(ref.input), "\n")
	if content == "" {
		e.emitOutputBlock(marker, nil)
		return
	}

	plain := strings.Split(content, "\n")
	hi := indexHighlightedLines(filePathOf(ref.input), content)

	lines := make([]ui.Line, len(plain))
	for i, raw := range plain {
		body := pickHighlighted(hi, i, strings.TrimRight(raw, " \t\r"))
		lines[i] = ui.Line{Text: "+ " + body, Color: ui.DiffAddBg}
	}
	e.emitOutputBlock(marker, lines)
}

// extractContentText pulls plain text out of a tool_result block's
// `content` field, which the claude CLI encodes either as a bare JSON
// string or as an array of {type:"text", text:"..."} objects. Returns
// an empty string for any other shape.
func extractContentText(b stream.Block) string {
	if len(b.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		return s
	}
	var arr []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b.Content, &arr); err == nil {
		var sb strings.Builder
		for _, item := range arr {
			sb.WriteString(item.Text)
		}
		return sb.String()
	}
	return ""
}

// stripLineNumber removes a leading `cat -n`-style line-number prefix
// (optional spaces, digits, single tab) from s. Lines that don't match
// the pattern pass through unchanged, so non-Read consumers and any
// content that legitimately starts with whitespace and digits stay
// intact.
func stripLineNumber(s string) string {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	digitStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == digitStart || i >= len(s) || s[i] != '\t' {
		return s
	}
	return s[i+1:]
}

// bashCommand pulls the `command` field from a Bash tool_use input.
// Returns an empty string if the field is missing or the JSON is
// malformed; the renderer then prints a bare arrow rather than crash.
func bashCommand(input json.RawMessage) string {
	var s struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &s)
	return s.Command
}

// writeContent pulls the `content` field from a Write tool_use
// input. Same fail-soft semantics as [bashCommand].
func writeContent(input json.RawMessage) string {
	var s struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(input, &s)
	return s.Content
}

// filePathOf pulls the `file_path` field from a tool_use input.
// Shared by the Read and Edit renderers (and any other tool that
// keys on a single path). Same fail-soft semantics as [bashCommand].
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
// at [outputLineCap] visible lines with a `...` truncation marker
// when the body runs longer, and shares the same gutter/wrap rules
// as every other block.
func (e *emitter) emitUserText(text string) {
	body := strings.TrimRight(text, "\n")
	var lines []ui.Line
	if body != "" {
		for _, l := range strings.Split(body, "\n") {
			lines = append(lines, ui.Line{Text: strings.TrimRight(l, " \t\r"), Color: ui.Orange})
		}
	}
	e.emitOutputBlock("→", lines)
}

// onResult handles the iteration's terminal "result" event: it
// records token usage and prints the result line with status, turn
// count, claude's own duration estimate and self-reported cost.
func (e *emitter) onResult(r stream.Result) {
	e.stats.trackUsage(r.Usage)

	bits := []string{}
	if status := decodeStatus(r.StructuredOutput); status != "" {
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

	marker := "←" // success/error variants were: ✓ / ✗
	if r.IsError {
		marker = "←"
	}
	content := "result"
	if len(bits) > 0 {
		content += "  " + strings.Join(bits, "  ")
	}
	ui.Decorate(e.out, marker, content)
}

// onSystem handles "system" events: session boot, model selection,
// permission mode, tool list, etc.
func (e *emitter) onSystem(s stream.System) {
	if !e.verbose {
		return
	}
	subtype := s.Subtype
	if subtype == "" {
		subtype = "system"
	}

	bits := []string{}
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
	ui.Decorate(e.out, "←", content) // was: #
}

// onRateLimit reports the rate-limit envelope claude attaches to some
// events. The fields surfaced match the Ruby driver's set.
func (e *emitter) onRateLimit(r stream.RateLimit) {
	if !e.verbose {
		return
	}
	info := r.Info
	if info == nil {
		info = &stream.RateLimitInfo{}
	}

	bits := []string{}
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
	ui.Decorate(e.out, "←", content) // was: ⚠
}

// decodeStatus extracts the schema-constrained status field from a
// raw structured-output payload. Returns an empty string for any
// shape that doesn't match the expected envelope.
func decodeStatus(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var so stream.StatusOutput
	if err := json.Unmarshal(raw, &so); err != nil {
		return ""
	}
	return so.Status
}
