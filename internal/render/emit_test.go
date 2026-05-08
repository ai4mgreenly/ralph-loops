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
// timing attribution and token tallies.
type fakeRecorder struct {
	blocks   map[string]int
	llmTime  time.Duration
	toolTime time.Duration
	tokens   struct {
		input       int
		output      int
		cacheRead   int
		cacheCreate int
	}
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{blocks: make(map[string]int)}
}

func (f *fakeRecorder) TallyBlock(t string)         { f.blocks[t]++ }
func (f *fakeRecorder) AddLLMTime(d time.Duration)  { f.llmTime += d }
func (f *fakeRecorder) AddToolTime(d time.Duration) { f.toolTime += d }
func (f *fakeRecorder) TrackUsage(u *stream.Usage) {
	if u == nil {
		return
	}
	f.tokens.input += u.InputTokens
	f.tokens.output += u.OutputTokens
	f.tokens.cacheRead += u.CacheReadInputTokens
	f.tokens.cacheCreate += u.CacheCreationInputTokens
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

func TestReadTarget(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no range",
			input: `{"file_path":"foo.go"}`,
			want:  "foo.go",
		},
		{
			name:  "offset and limit",
			input: `{"file_path":"foo.go","offset":200,"limit":50}`,
			want:  "foo.go:200-249",
		},
		{
			name:  "offset only",
			input: `{"file_path":"foo.go","offset":200}`,
			want:  "foo.go:200-",
		},
		{
			name:  "limit only",
			input: `{"file_path":"foo.go","limit":50}`,
			want:  "foo.go:1-50",
		},
		{
			name:  "limit one",
			input: `{"file_path":"foo.go","limit":1}`,
			want:  "foo.go:1-1",
		},
		{
			name:  "missing file_path stays empty but range still renders",
			input: `{"offset":10,"limit":5}`,
			want:  ":10-14",
		},
		{
			name:  "garbage input is fail-soft",
			input: `not json`,
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := readTarget(json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("readTarget(%s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestIterationBanner(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.IterationBanner(3)

	out := buf.String()
	if !strings.Contains(out, "iteration: 3") {
		t.Errorf("banner missing iteration line: %q", out)
	}
	if !strings.Contains(out, ruleChar+ruleChar+ruleChar) {
		t.Errorf("banner missing horizontal rule: %q", out)
	}
	// Banner must have the rule both before and after the iteration line.
	idx := strings.Index(out, "iteration: 3")
	if idx < 0 {
		t.Fatal("iteration line missing")
	}
	if !strings.Contains(out[:idx], ruleChar) {
		t.Errorf("rule should appear above the iteration line, got %q", out[:idx])
	}
	if !strings.Contains(out[idx:], ruleChar) {
		t.Errorf("rule should appear below the iteration line, got %q", out[idx:])
	}
}

// newTestEmitter builds an Emitter writing into a fresh buffer with a
// deterministic clock advancing by 1ms per call. Tests should call
// ResetIteration() before invoking the On* methods.
func newTestEmitter(t *testing.T) (*Emitter, *bytes.Buffer, *fakeRecorder) {
	t.Helper()
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(false) })

	var buf bytes.Buffer
	rec := newFakeRecorder()
	e := NewEmitter(&buf, rec)
	e.now = fakeClock(time.Unix(0, 0).UTC(), time.Millisecond)
	e.ResetIteration()
	return e, &buf, rec
}

func TestEmitter_AssistantTextLeadsWithMarker(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockText, Text: "hello\nworld"},
	}}})
	got := buf.String()
	if !strings.Contains(got, "←  hello\n") || !strings.Contains(got, "   world\n") {
		t.Errorf("assistant text missing lead marker / continuation indent:\n%s", got)
	}
}

func TestEmitter_ToolUseRecordsRefAndPrintsCall(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
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
	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"echo hi"}`),
		},
	}}})
	buf.Reset()

	e.OnUser(stream.User{
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

	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
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

	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
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

	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
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

	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
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
	e, buf, rec := newTestEmitter(t)
	e.OnResult(stream.Result{
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
	if rec.tokens.input != 100 || rec.tokens.output != 50 {
		t.Errorf("tokens not tracked: %+v", rec.tokens)
	}
}

func TestEmitter_TimingAttribution(t *testing.T) {
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(false) })

	var buf bytes.Buffer
	rec := newFakeRecorder()
	e := NewEmitter(&buf, rec)

	now := time.Unix(0, 0).UTC()
	clock := func(advance time.Duration) {
		now = now.Add(advance)
	}
	e.now = func() time.Time { return now }
	e.ResetIteration()

	clock(2 * time.Second) // 2s of LLM work before assistant arrives
	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockText, Text: "thinking done"},
	}}})

	clock(time.Second) // 1s of tool work before user arrives
	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "x"},
	}}})

	if rec.llmTime != 2*time.Second {
		t.Errorf("llmTime = %v, want 2s", rec.llmTime)
	}
	if rec.toolTime != time.Second {
		t.Errorf("toolTime = %v, want 1s", rec.toolTime)
	}
}

func TestEmitter_SystemAndRateLimit(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.Verbose = true
	e.OnSystem(stream.System{
		Subtype: "init", Model: "opus", PermissionMode: "default",
		Tools: []string{"Bash", "Read"},
	})
	if got := buf.String(); !strings.Contains(got, "←  init  model=opus  perm=default  tools=2") {
		t.Errorf("system line wrong:\n%s", got)
	}
	buf.Reset()

	e.OnRateLimit(stream.RateLimit{Info: &stream.RateLimitInfo{
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
	// Verbose defaults to false.
	e.OnSystem(stream.System{Subtype: "init", Model: "opus"})
	e.OnRateLimit(stream.RateLimit{Info: &stream.RateLimitInfo{RateLimitType: "weekly"}})
	if got := buf.String(); got != "" {
		t.Errorf("non-verbose run leaked low-signal events:\n%s", got)
	}
}

// TestEmitter_BashErrorUsesErrorMarker pins the visual distinction
// between a successful tool result and a failed one: a bash result
// flagged IsError must lead with [markerError], not [markerResult].
func TestEmitter_BashErrorUsesErrorMarker(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	ui.SetTerminalWidth(0)
	t.Cleanup(func() { ui.SetTerminalWidth(0) })

	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"false"}`),
		},
	}}})
	buf.Reset()

	e.OnUser(stream.User{
		Message: stream.Message{Content: []stream.Block{
			{Type: stream.BlockToolResult, ToolUseID: "tool_1", IsError: true},
		}},
		ToolUseResult: json.RawMessage(`{"stdout":"","stderr":"boom\n"}`),
	})

	got := buf.String()
	if !strings.Contains(got, markerError) {
		t.Errorf("error result missing markerError %q:\n%s", markerError, got)
	}
	// Make sure the success marker is not what's leading the result line.
	// markerResult is also used elsewhere; this checks the gutter.
	if strings.Contains(got, markerResult+"  boom") {
		t.Errorf("error result should not lead stderr line with markerResult %q:\n%s", markerResult, got)
	}
}

// TestEmitter_UnknownToolErrorUsesErrorMarker pins the same behaviour
// for the generic emitToolResult path used by tools without a
// dedicated renderer.
func TestEmitter_UnknownToolErrorUsesErrorMarker(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Grep",
			Input: json.RawMessage(`{"pattern":"x"}`),
		},
	}}})
	buf.Reset()

	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "tool_1", IsError: true},
	}}})

	got := buf.String()
	if !strings.Contains(got, markerError) {
		t.Errorf("error result missing markerError %q:\n%s", markerError, got)
	}
	if strings.Contains(got, "ERR") == false {
		t.Errorf("expected ERR status in output:\n%s", got)
	}
}
