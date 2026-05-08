package loop

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// fakeClock returns a closure suitable for emitter.now that advances
// by step on every call, starting at base.
func fakeClock(base time.Time, step time.Duration) func() time.Time {
	t := base
	return func() time.Time {
		out := t
		t = t.Add(step)
		return out
	}
}

func TestIterationBanner(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.iterationBanner(3)

	out := buf.String()
	if !strings.Contains(out, "iteration: 3") {
		t.Errorf("banner missing iteration line: %q", out)
	}
	if !strings.Contains(out, statsRuleChar+statsRuleChar+statsRuleChar) {
		t.Errorf("banner missing horizontal rule: %q", out)
	}
	// Banner must have the rule both before and after the iteration line.
	idx := strings.Index(out, "iteration: 3")
	if idx < 0 {
		t.Fatal("iteration line missing")
	}
	if !strings.Contains(out[:idx], statsRuleChar) {
		t.Errorf("rule should appear above the iteration line, got %q", out[:idx])
	}
	if !strings.Contains(out[idx:], statsRuleChar) {
		t.Errorf("rule should appear below the iteration line, got %q", out[idx:])
	}
}

// newTestEmitter builds an emitter writing into a fresh buffer with a
// deterministic clock advancing by 1ms per call. Tests should call
// resetIteration() before invoking the on* methods.
func newTestEmitter(t *testing.T) (*emitter, *bytes.Buffer, *stats) {
	t.Helper()
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(false) })

	var buf bytes.Buffer
	s := newStats("opus")
	e := newEmitter(&buf, s)
	e.now = fakeClock(time.Unix(0, 0).UTC(), time.Millisecond)
	e.resetIteration()
	return e, &buf, s
}

func TestEmitter_AssistantTextLeadsWithMarker(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.onAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockText, Text: "hello\nworld"},
	}}})
	got := buf.String()
	if !strings.Contains(got, "←  hello\n") || !strings.Contains(got, "   world\n") {
		t.Errorf("assistant text missing lead marker / continuation indent:\n%s", got)
	}
}

func TestEmitter_ToolUseRecordsRefAndPrintsCall(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.onAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"ls"}`),
		},
	}}})

	if got := buf.String(); !strings.Contains(got, "←  ls\n") {
		t.Errorf("expected verbatim bash command line, got:\n%s", got)
	}
	if _, ok := e.tools["tool_1"]; !ok {
		t.Errorf("tool_1 should be in pending tools ledger")
	}
}

func TestEmitter_ToolResultPairsWithCall(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.onAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"echo hi"}`),
		},
	}}})
	buf.Reset()

	e.onUser(stream.User{
		Message: stream.Message{Content: []stream.Block{
			{Type: stream.BlockToolResult, ToolUseID: "tool_1"},
		}},
		ToolUseResult: json.RawMessage(`{"stdout":"hi\n","stderr":""}`),
	})

	got := buf.String()
	if !strings.Contains(got, "→  hi\n") {
		t.Errorf("expected raw bash output line, got:\n%s", got)
	}
	if _, ok := e.tools["tool_1"]; ok {
		t.Errorf("tool_1 should have been removed from ledger after result")
	}
}

func TestEmitter_EditRendersDiff(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	ui.SetTerminalWidth(0)
	t.Cleanup(func() { ui.SetTerminalWidth(0) })

	e.onAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Edit",
			Input: json.RawMessage(`{"file_path":"/tmp/x.go","old_string":"a\nb\nc","new_string":"a\nB\nc"}`),
		},
	}}})
	if got := buf.String(); !strings.Contains(got, "←  Edit  /tmp/x.go\n") {
		t.Errorf("expected edit call line, got:\n%s", got)
	}
	buf.Reset()

	e.onUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "tool_1"},
	}}})
	got := buf.String()
	for _, want := range []string{"→    a\n", "   - b\n", "   + B\n", "     c\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in diff output:\n%s", want, got)
		}
	}
}

func TestEmitter_WriteRendersContent(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	ui.SetTerminalWidth(0)
	t.Cleanup(func() { ui.SetTerminalWidth(0) })

	e.onAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Write",
			Input: json.RawMessage(`{"file_path":"/tmp/x.go","content":"package x\n\nfunc Hi() {}\n"}`),
		},
	}}})
	if got := buf.String(); !strings.Contains(got, "←  Write  /tmp/x.go\n") {
		t.Errorf("expected write call line, got:\n%s", got)
	}
	buf.Reset()

	e.onUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "tool_1"},
	}}})
	got := buf.String()
	for _, want := range []string{"→  + package x\n", "   + func Hi() {}\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in write output:\n%s", want, got)
		}
	}
}

func TestEmitter_ResultExtractsStatusAndTracksTokens(t *testing.T) {
	e, buf, s := newTestEmitter(t)
	e.onResult(stream.Result{
		NumTurns:         3,
		DurationMS:       2500,
		TotalCostUSD:     0.0234,
		Usage:            &stream.Usage{InputTokens: 100, OutputTokens: 50},
		StructuredOutput: json.RawMessage(`{"status":"CONTINUE"}`),
	})
	got := buf.String()
	for _, want := range []string{"status=CONTINUE", "turns=3", "duration=2.5s", "cost=$0.0234"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in result line:\n%s", want, got)
		}
	}
	if s.tokens.input != 100 || s.tokens.output != 50 {
		t.Errorf("tokens not tracked: %+v", s.tokens)
	}
}

func TestEmitter_TimingAttribution(t *testing.T) {
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(false) })

	var buf bytes.Buffer
	s := newStats("opus")
	e := newEmitter(&buf, s)

	now := time.Unix(0, 0).UTC()
	clock := func(advance time.Duration) {
		now = now.Add(advance)
	}
	e.now = func() time.Time { return now }
	e.resetIteration()

	clock(2 * time.Second) // 2s of LLM work before assistant arrives
	e.onAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockText, Text: "thinking done"},
	}}})

	clock(time.Second) // 1s of tool work before user arrives
	e.onUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "x"},
	}}})

	if s.llmTime != 2*time.Second {
		t.Errorf("llmTime = %v, want 2s", s.llmTime)
	}
	if s.toolTime != time.Second {
		t.Errorf("toolTime = %v, want 1s", s.toolTime)
	}
}

func TestEmitter_SystemAndRateLimit(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.verbose = true
	e.onSystem(stream.System{
		Subtype: "init", Model: "opus", PermissionMode: "default",
		Tools: []string{"Bash", "Read"},
	})
	if got := buf.String(); !strings.Contains(got, "←  init  model=opus  perm=default  tools=2") {
		t.Errorf("system line wrong:\n%s", got)
	}
	buf.Reset()

	e.onRateLimit(stream.RateLimit{Info: &stream.RateLimitInfo{
		RateLimitType:  "weekly",
		Status:         "warning",
		Utilization:    0.85,
		IsUsingOverage: true,
	}})
	got := buf.String()
	for _, want := range []string{"←  rate_limit", "type=weekly", "status=warning", "util=85.0%", "overage"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in rate-limit line:\n%s", want, got)
		}
	}
}

func TestEmitter_NonVerboseSuppressesSystemAndRateLimit(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	// verbose defaults to false.
	e.onSystem(stream.System{Subtype: "init", Model: "opus"})
	e.onRateLimit(stream.RateLimit{Info: &stream.RateLimitInfo{RateLimitType: "weekly"}})
	if got := buf.String(); got != "" {
		t.Errorf("non-verbose run leaked low-signal events:\n%s", got)
	}
}
