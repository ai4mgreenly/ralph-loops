package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// fakeRecorder is a minimal in-memory [Recorder] used by the emit
// tests. It mirrors just enough of the loop's stats type to assert on
// timing attribution, event/block tallies, captured usage, and tool
// outcomes.
type fakeRecorder struct {
	events   map[string]int
	blocks   map[string]int
	llmTime  time.Duration
	toolTime time.Duration

	usages []capturedUsage
	tools  []toolOutcome
}

// capturedUsage records one TrackMessageUsage call so tests can assert
// the provider / effective-model / stop-reason wiring the partial
// fallback depends on.
type capturedUsage struct {
	usage      *stream.Usage
	provider   string
	model      string
	stopReason string
}

// toolOutcome records one TrackToolOutcome call so tests can assert the
// tool-call and tool-error counts stats derive from this channel.
type toolOutcome struct {
	name    string
	isError bool
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{
		events: make(map[string]int),
		blocks: make(map[string]int),
	}
}

func (f *fakeRecorder) TallyEvent(kind string)      { f.events[kind]++ }
func (f *fakeRecorder) TallyBlock(t string)         { f.blocks[t]++ }
func (f *fakeRecorder) AddLLMTime(d time.Duration)  { f.llmTime += d }
func (f *fakeRecorder) AddToolTime(d time.Duration) { f.toolTime += d }
func (f *fakeRecorder) TrackMessageUsage(u *stream.Usage, provider, model, stopReason string) {
	f.usages = append(f.usages, capturedUsage{u, provider, model, stopReason})
}
func (f *fakeRecorder) TrackToolOutcome(name string, isError bool) {
	f.tools = append(f.tools, toolOutcome{name, isError})
}

// fakeClock returns a closure suitable for Emitter.now that advances
// by step on every call, starting at base.
func fakeClock(base time.Time, step time.Duration) func() time.Time {
	t := base
	return func() time.Time {
		out := t
		t = t.Add(step)
		return out
	}
}

// newTestEmitter builds an Emitter writing into a fresh buffer with a
// deterministic clock advancing by 1ms per call. Tests should call
// ResetIteration() before invoking the dispatch methods (this helper
// already does).
func newTestEmitter(t *testing.T) (*Emitter, *bytes.Buffer, *fakeRecorder) {
	t.Helper()
	var buf bytes.Buffer
	rec := newFakeRecorder()
	theme := ui.NewThemeWith(false, 0)
	e := NewEmitter(&buf, rec, theme)
	e.now = fakeClock(time.Unix(0, 0).UTC(), time.Millisecond)
	e.ResetIteration()
	return e, &buf, rec
}

// assistantText is a tiny constructor for an assistant [stream.MessageEnd]
// carrying a single text block.
func assistantText(text string) stream.MessageEnd {
	return stream.MessageEnd{Message: stream.PiMessage{
		Role:    stream.RoleAssistant,
		Content: []stream.ContentBlock{{Type: stream.BlockText, Text: text}},
	}}
}

func TestIterationBanner(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.IterationBanner(3)

	out := buf.String()
	if !strings.Contains(out, "iteration: 3") {
		t.Errorf("banner missing iteration line: %q", out)
	}
	if !strings.Contains(out, ui.RuleChar+ui.RuleChar+ui.RuleChar) {
		t.Errorf("banner missing horizontal rule: %q", out)
	}
	idx := strings.Index(out, "iteration: 3")
	if idx < 0 {
		t.Fatal("iteration line missing")
	}
	if !strings.Contains(out[:idx], ui.RuleChar) {
		t.Errorf("rule should appear above the iteration line, got %q", out[:idx])
	}
	if !strings.Contains(out[idx:], ui.RuleChar) {
		t.Errorf("rule should appear below the iteration line, got %q", out[idx:])
	}
}

// TestEmitter_AssistantTextLeadsWithMarker pins the prose path: an
// assistant text message_end leads with the call marker and indents
// continuation lines under the gutter.
func TestEmitter_AssistantTextLeadsWithMarker(t *testing.T) {
	e, buf, rec := newTestEmitter(t)
	e.OnEvent(assistantText("hello\nworld"))

	got := buf.String()
	if !strings.Contains(got, "←  hello\n") || !strings.Contains(got, "   world\n") {
		t.Errorf("assistant text missing lead marker / continuation indent:\n%s", got)
	}
	if rec.events[stream.TypeMessageEnd] != 1 {
		t.Errorf("message_end not tallied by kind: %v", rec.events)
	}
	if rec.blocks[stream.BlockText] != 1 {
		t.Errorf("text block not tallied: %v", rec.blocks)
	}
}

// TestEmitter_ToolResultMessageEndDropped pins the locked de-dupe
// decision: a message_end with role "toolResult" is dropped entirely
// — not rendered, not tallied as a block (it is still tallied as an
// event by kind, like every line).
func TestEmitter_ToolResultMessageEndDropped(t *testing.T) {
	e, buf, rec := newTestEmitter(t)
	e.OnEvent(stream.MessageEnd{Message: stream.PiMessage{
		Role:       stream.RoleToolResult,
		ToolCallID: "call_1",
		ToolName:   "edit",
		Content:    []stream.ContentBlock{{Type: stream.BlockText, Text: "Successfully replaced"}},
	}})

	if got := buf.String(); got != "" {
		t.Errorf("toolResult message_end must render nothing, got:\n%s", got)
	}
	if len(rec.blocks) != 0 {
		t.Errorf("toolResult blocks must not be tallied, got: %v", rec.blocks)
	}
	if rec.events[stream.TypeMessageEnd] != 1 {
		t.Errorf("event still tallied by kind exactly once: %v", rec.events)
	}
}

// TestEmitter_GenericToolHeaderAndResult exercises the single B-lite
// renderer: a tool_execution_start prints `toolName  <primary arg>`,
// and the matching tool_execution_end prints result.content[] text.
func TestEmitter_GenericToolHeaderAndResult(t *testing.T) {
	e, buf, rec := newTestEmitter(t)

	e.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "call_1",
		ToolName:   "bash",
		Args:       json.RawMessage(`{"command":"echo hi"}`),
	})
	if got := buf.String(); !strings.Contains(got, "←  bash  echo hi\n") {
		t.Errorf("expected B-lite tool header, got:\n%s", got)
	}
	buf.Reset()

	e.OnEvent(stream.ToolExecutionEnd{
		ToolCallID: "call_1",
		ToolName:   "bash",
		Result:     json.RawMessage(`{"content":[{"type":"text","text":"hi\n"}]}`),
	})
	got := buf.String()
	if !strings.Contains(got, "→  hi\n") {
		t.Errorf("expected result content text, got:\n%s", got)
	}
	if _, ok := e.tools["call_1"]; ok {
		t.Errorf("call_1 should have been removed from the pending ledger")
	}
	if len(rec.tools) != 1 || rec.tools[0] != (toolOutcome{"bash", false}) {
		t.Errorf("tool outcome not recorded as expected: %v", rec.tools)
	}
}

// TestEmitter_ReadToolPrimaryArgIsPath confirms the primary-arg
// precedence: a `read` start carries `path`, which becomes the header
// suffix, and its result content is rendered verbatim.
func TestEmitter_ReadToolPrimaryArgIsPath(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "r1",
		ToolName:   "read",
		Args:       json.RawMessage(`{"path":"/tmp/sample.txt"}`),
	})
	e.OnEvent(stream.ToolExecutionEnd{
		ToolCallID: "r1",
		ToolName:   "read",
		Result:     json.RawMessage(`{"content":[{"type":"text","text":"alpha\nbeta\ngamma\n"}]}`),
	})

	got := buf.String()
	if !strings.Contains(got, "←  read  /tmp/sample.txt\n") {
		t.Errorf("expected read header with path, got:\n%s", got)
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in read result:\n%s", want, got)
		}
	}
}

// TestEmitter_EditRendersDiff is the locked-decision acceptance test:
// a toolName=="edit" start whose Args is
// {"path":"f","edits":[{"oldText":"quick","newText":"slow"}]} must
// produce a reconstructed diff (removed `quick`, added `slow`) when the
// matching end arrives — NOT the result.content[] text.
func TestEmitter_EditRendersDiff(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "call_edit",
		ToolName:   "edit",
		Args:       json.RawMessage(`{"path":"f","edits":[{"oldText":"quick","newText":"slow"}]}`),
	})
	if got := buf.String(); !strings.Contains(got, "←  edit  f\n") {
		t.Errorf("expected edit header, got:\n%s", got)
	}
	buf.Reset()

	e.OnEvent(stream.ToolExecutionEnd{
		ToolCallID: "call_edit",
		ToolName:   "edit",
		Result:     json.RawMessage(`{"content":[{"type":"text","text":"Successfully replaced 1 block(s)."}],"details":{"diff":"-1 quick\n+1 slow"}}`),
	})
	got := buf.String()
	// Diff structure: removed `quick` line, added `slow` line. We assert
	// the stable rendered shape, not byte-identical output.
	if !strings.Contains(got, "- quick") {
		t.Errorf("expected removed-line `- quick` in diff:\n%s", got)
	}
	if !strings.Contains(got, "+ slow") {
		t.Errorf("expected added-line `+ slow` in diff:\n%s", got)
	}
	// The result.content[] text must NOT leak through for `edit`.
	if strings.Contains(got, "Successfully replaced") {
		t.Errorf("edit must render the diff, not result.content text:\n%s", got)
	}
}

// TestEmitter_EditMultiHunkDiff pins multi-block reconstruction: every
// edits[] entry is diffed in order and concatenated into one block.
func TestEmitter_EditMultiHunkDiff(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "call_edit",
		ToolName:   "edit",
		Args: json.RawMessage(`{"path":"f.go","edits":[` +
			`{"oldText":"a\nb\nc","newText":"a\nB\nc"},` +
			`{"oldText":"x","newText":"y"}]}`),
	})
	buf.Reset()
	e.OnEvent(stream.ToolExecutionEnd{ToolCallID: "call_edit", ToolName: "edit",
		Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)})

	got := buf.String()
	for _, want := range []string{"- b", "+ B", "- x", "+ y"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in multi-hunk diff:\n%s", want, got)
		}
	}
}

// TestEmitter_ToolErrorUsesErrorMarkerAndStyling pins the visual
// distinction: a tool_execution_end with isError must lead with
// [markerError] and style the body in dim-red, not the success path.
func TestEmitter_ToolErrorUsesErrorMarkerAndStyling(t *testing.T) {
	var buf bytes.Buffer
	rec := newFakeRecorder()
	theme := ui.NewThemeWith(true, 0) // color on, so we can see styling
	e := NewEmitter(&buf, rec, theme)
	e.now = fakeClock(time.Unix(0, 0).UTC(), time.Millisecond)
	e.ResetIteration()

	e.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "call_1", ToolName: "bash",
		Args: json.RawMessage(`{"command":"false"}`),
	})
	buf.Reset()

	e.OnEvent(stream.ToolExecutionEnd{
		ToolCallID: "call_1", ToolName: "bash", IsError: true,
		Result: json.RawMessage(`{"content":[{"type":"text","text":"boom"}]}`),
	})

	got := buf.String()
	if !strings.Contains(got, markerError) {
		t.Errorf("error result missing markerError %q:\n%q", markerError, got)
	}
	// dim-red escape must wrap the body when isError.
	if !strings.Contains(got, "\x1b[2;31m") {
		t.Errorf("error body should be dim-red styled, got:\n%q", got)
	}
	if len(rec.tools) != 1 || !rec.tools[0].isError {
		t.Errorf("tool error outcome not recorded: %v", rec.tools)
	}
}

// TestEmitter_ToolEmptyResultRendersNothing pins the regression where
// a result with no text content rendered as a bare marker on its own
// line. A successful empty result must produce no output at all.
func TestEmitter_ToolEmptyResultRendersNothing(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "call_1", ToolName: "bash",
		Args: json.RawMessage(`{"command":"true"}`),
	})
	buf.Reset()

	e.OnEvent(stream.ToolExecutionEnd{
		ToolCallID: "call_1", ToolName: "bash",
		Result: json.RawMessage(`{"content":[]}`),
	})

	if got := buf.String(); got != "" {
		t.Errorf("empty successful result should render nothing, got:\n%s", got)
	}
}

// TestEmitter_ToolEmptyErrorResultStillMarks confirms an errored tool
// with no body still earns a visible marker so the failure is not
// silently swallowed.
func TestEmitter_ToolEmptyErrorResultStillMarks(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "call_1", ToolName: "grep",
		Args: json.RawMessage(`{"pattern":"x"}`),
	})
	buf.Reset()

	e.OnEvent(stream.ToolExecutionEnd{
		ToolCallID: "call_1", ToolName: "grep", IsError: true,
		Result: json.RawMessage(`{"content":[]}`),
	})

	got := buf.String()
	if !strings.Contains(got, markerError) {
		t.Errorf("errored empty result must still mark the failure, got:\n%s", got)
	}
	if !strings.Contains(got, "grep error") {
		t.Errorf("expected a named error line, got:\n%s", got)
	}
}

// TestEmitter_TimingAttribution pins ralph's own wall-clock split: the
// gap before an assistant message_end is LLM time; the start→end span
// of a tool execution is tool time.
func TestEmitter_TimingAttribution(t *testing.T) {
	var buf bytes.Buffer
	rec := newFakeRecorder()
	theme := ui.NewThemeWith(false, 0)
	e := NewEmitter(&buf, rec, theme)

	now := time.Unix(0, 0).UTC()
	clock := func(advance time.Duration) { now = now.Add(advance) }
	e.now = func() time.Time { return now }
	e.ResetIteration()

	clock(2 * time.Second) // 2s of LLM work before the assistant message settles
	e.OnEvent(assistantText("done thinking"))

	clock(time.Second) // tool starts 1s later
	e.OnEvent(stream.ToolExecutionStart{ToolCallID: "x", ToolName: "bash",
		Args: json.RawMessage(`{"command":"sleep"}`)})
	clock(3 * time.Second) // tool runs 3s
	e.OnEvent(stream.ToolExecutionEnd{ToolCallID: "x", ToolName: "bash",
		Result: json.RawMessage(`{"content":[]}`)})

	if rec.llmTime != 2*time.Second {
		t.Errorf("llmTime = %v, want 2s", rec.llmTime)
	}
	if rec.toolTime != 3*time.Second {
		t.Errorf("toolTime = %v, want 3s (start→end span)", rec.toolTime)
	}
}

// TestEmitter_AssistantUsageCaptured pins the partial-fallback wiring:
// an assistant message_end's usage is forwarded with the provider, the
// effective model, and the stop reason. ResponseModel wins over Model
// when present.
func TestEmitter_AssistantUsageCaptured(t *testing.T) {
	e, _, rec := newTestEmitter(t)

	u := &stream.Usage{Input: 380, Output: 11, TotalTokens: 391}
	e.OnEvent(stream.MessageEnd{Message: stream.PiMessage{
		Role:          stream.RoleAssistant,
		Content:       []stream.ContentBlock{{Type: stream.BlockText, Text: "ok"}},
		Provider:      "openai-codex",
		Model:         "gpt-5.3-codex",
		ResponseModel: "gpt-5.3-codex-2026",
		StopReason:    "stop",
		Usage:         u,
	}})

	if len(rec.usages) != 1 {
		t.Fatalf("expected one captured usage, got %d", len(rec.usages))
	}
	cu := rec.usages[0]
	if cu.usage != u {
		t.Errorf("usage pointer not forwarded: %#v", cu.usage)
	}
	if cu.provider != "openai-codex" {
		t.Errorf("provider = %q, want openai-codex", cu.provider)
	}
	if cu.model != "gpt-5.3-codex-2026" {
		t.Errorf("effective model = %q, want responseModel gpt-5.3-codex-2026", cu.model)
	}
	if cu.stopReason != "stop" {
		t.Errorf("stopReason = %q, want stop", cu.stopReason)
	}
}

// TestEmitter_EffectiveModelFallsBackToModel confirms the effective
// model is the requested Model when ResponseModel is absent.
func TestEmitter_EffectiveModelFallsBackToModel(t *testing.T) {
	e, _, rec := newTestEmitter(t)
	e.OnEvent(stream.MessageEnd{Message: stream.PiMessage{
		Role:    stream.RoleAssistant,
		Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "ok"}},
		Model:   "gpt-5.3-codex",
	}})
	if len(rec.usages) != 1 || rec.usages[0].model != "gpt-5.3-codex" {
		t.Errorf("effective model should fall back to Model, got %#v", rec.usages)
	}
}

// TestEmitter_SessionVerboseGate pins the low-signal gate: the session
// banner is suppressed by default and surfaces only under verbose.
func TestEmitter_SessionVerboseGate(t *testing.T) {
	e, buf, rec := newTestEmitter(t)
	e.OnEvent(stream.Session{Version: 1, ID: "sess-1", Cwd: "/work"})
	if got := buf.String(); got != "" {
		t.Errorf("non-verbose run leaked the session banner:\n%s", got)
	}
	if rec.events[stream.TypeSession] != 1 {
		t.Errorf("session still tallied by kind: %v", rec.events)
	}

	e.verbose = true
	e.OnEvent(stream.Session{Version: 1, ID: "sess-1", Cwd: "/work"})
	got := buf.String()
	for _, want := range []string{"session", "version=1", "id=sess-1", "cwd=/work"} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose session missing %q:\n%s", want, got)
		}
	}
}

// TestEmitter_UnrenderedKindsAreTallied confirms the known-but-unused
// carriers and turn_end are tallied by kind but produce no output: the
// authoritative tally comes from agent_end (read by the loop, not here).
func TestEmitter_UnrenderedKindsAreTallied(t *testing.T) {
	e, buf, rec := newTestEmitter(t)
	e.OnEvent(stream.KnownEvent{Type: "message_update"})
	e.OnEvent(stream.TurnEnd{})
	e.OnEvent(stream.AgentEnd{})
	e.OnEvent(stream.UnknownEvent{Type: "future_event"})

	if got := buf.String(); got != "" {
		t.Errorf("unrendered kinds must produce no output, got:\n%s", got)
	}
	for _, k := range []string{"message_update", stream.TypeTurnEnd, stream.TypeAgentEnd, "future_event"} {
		if rec.events[k] != 1 {
			t.Errorf("kind %q not tallied exactly once: %v", k, rec.events)
		}
	}
}

// TestEmitter_NilEventIsSafe guards the dispatch against a nil event
// (the stream package can return a nil event alongside a fatal read
// error); it must not panic or tally.
func TestEmitter_NilEventIsSafe(t *testing.T) {
	e, buf, rec := newTestEmitter(t)
	e.OnEvent(nil)
	if got := buf.String(); got != "" {
		t.Errorf("nil event should render nothing, got:\n%s", got)
	}
	if len(rec.events) != 0 {
		t.Errorf("nil event should not be tallied: %v", rec.events)
	}
}

// TestEmitter_UserMessageRendersKickoffReplay pins the user-role path:
// pi replays the kickoff prompt as a user message_end; it renders as a
// tucked-under block under the result marker.
func TestEmitter_UserMessageRendersKickoffReplay(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.OnEvent(stream.MessageEnd{Message: stream.PiMessage{
		Role:    stream.RoleUser,
		Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "first line\nsecond line\n"}},
	}})
	got := buf.String()
	if !strings.Contains(got, "→  first line") {
		t.Errorf("expected leading marker on first user line, got:\n%s", got)
	}
	if !strings.Contains(got, "second line") {
		t.Errorf("expected continuation line in user text, got:\n%s", got)
	}
}

// TestEmitter_DecodeErrorSurfaced confirms a decode error reaches the
// activity log with the line number and escaped raw bytes.
func TestEmitter_DecodeErrorSurfaced(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.OnDecodeError(stream.DecodeError{Line: 42, Bytes: []byte("garbage\x00")})
	got := buf.String()
	if !strings.Contains(got, "decode error: line 42") {
		t.Errorf("decode error not surfaced:\n%s", got)
	}
}
