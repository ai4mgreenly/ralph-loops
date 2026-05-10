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
	if !strings.Contains(out, ui.RuleChar+ui.RuleChar+ui.RuleChar) {
		t.Errorf("banner missing horizontal rule: %q", out)
	}
	// Banner must have the rule both before and after the iteration line.
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

// newTestEmitter builds an Emitter writing into a fresh buffer with a
// deterministic clock advancing by 1ms per call. Tests should call
// ResetIteration() before invoking the On* methods.
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
	var buf bytes.Buffer
	rec := newFakeRecorder()
	theme := ui.NewThemeWith(false, 0)
	e := NewEmitter(&buf, rec, theme)

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
	e.verbose = true
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

// TestEmitter_BashEmptyResultRendersNothing pins the regression where
// a bash result with no captured stdout/stderr (common when the
// command fails before any output reaches the tool result envelope)
// was rendering as a bare error marker on its own line. Empty content
// must produce no output at all.
func TestEmitter_BashEmptyResultRendersNothing(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"ls /does/not/exist"}`),
		},
	}}})
	buf.Reset()

	e.OnUser(stream.User{
		Message: stream.Message{Content: []stream.Block{
			{Type: stream.BlockToolResult, ToolUseID: "tool_1", IsError: true},
		}},
		ToolUseResult: json.RawMessage(`{"stdout":"","stderr":""}`),
	})

	if got := buf.String(); got != "" {
		t.Errorf("empty bash result should render nothing, got:\n%s", got)
	}
}

// TestEmitter_BashResultFallsBackToBlockContent pins the
// alternate-engine path: when the user event carries no
// `tool_use_result` sidecar (the claude-CLI-specific extension), the
// renderer must fall back to the tool_result block's `content` field.
// Fixture mirrors what ikigai-cli emits when driving Google: a short
// alphanumeric tool_use ID and the joined output as a JSON string.
func TestEmitter_BashResultFallsBackToBlockContent(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "v7vo5hw6",
			Name:  "Bash",
			Input: json.RawMessage(`{"command":"./test.sh"}`),
		},
	}}})
	buf.Reset()

	contentJSON, err := json.Marshal("ok  \tralph-scoops\t(cached)\n\n[exit: 0]")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "v7vo5hw6", Content: contentJSON},
	}}})

	got := buf.String()
	for _, want := range []string{"ok", "ralph-scoops", "[exit: 0]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in fallback bash output:\n%s", want, got)
		}
	}
	if _, ok := e.tools["v7vo5hw6"]; ok {
		t.Errorf("short-ID tool entry should have been removed from ledger after result")
	}
}

// TestEmitter_ReadResultStripsLineNumbers exercises emitReadResult by
// pairing a Read tool_use with a tool_result whose `content` is the
// agent's `cat -n`-style line-numbered text. The result block must
// surface the bare source lines (no leading line number, no tab).
func TestEmitter_ReadResultStripsLineNumbers(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Read",
			Input: json.RawMessage(`{"file_path":"/tmp/x.txt"}`),
		},
	}}})
	buf.Reset()

	// `cat -n` style: spaces, digits, tab, source line.
	const numbered = "     1\tfirst line\n     2\tsecond line\n     3\tthird line"
	contentJSON, err := json.Marshal(numbered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "tool_1", Content: contentJSON},
	}}})

	got := buf.String()
	for _, want := range []string{"first line", "second line", "third line"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in Read result:\n%s", want, got)
		}
	}
	// And the literal "cat -n" prefix must NOT survive.
	for _, prefix := range []string{"     1\t", "     2\t", "     3\t"} {
		if strings.Contains(got, prefix) {
			t.Errorf("line-number prefix %q leaked through:\n%s", prefix, got)
		}
	}
}

// TestEmitter_ReadResultArrayContent feeds the alternative
// {"content":[{"type":"text","text":"..."}]} shape extractContentText
// supports and confirms the renderer concatenates the array's text
// fields.
func TestEmitter_ReadResultArrayContent(t *testing.T) {
	e, buf, _ := newTestEmitter(t)

	e.OnAssistant(stream.Assistant{Message: stream.Message{Content: []stream.Block{
		{
			Type:  stream.BlockToolUse,
			ID:    "tool_1",
			Name:  "Read",
			Input: json.RawMessage(`{"file_path":"/tmp/x.txt"}`),
		},
	}}})
	buf.Reset()

	contentJSON := json.RawMessage(`[{"type":"text","text":"alpha\n"},{"type":"text","text":"beta"}]`)
	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockToolResult, ToolUseID: "tool_1", Content: contentJSON},
	}}})

	got := buf.String()
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in Read array-content output:\n%s", want, got)
		}
	}
}

// TestExtractContentText covers the three shapes the helper supports:
// a bare JSON string, an array of {text} objects, and an unrecognised
// shape that yields the empty string.
func TestExtractContentText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   json.RawMessage
		want string
	}{
		{
			name: "empty content",
			in:   nil,
			want: "",
		},
		{
			name: "bare string",
			in:   json.RawMessage(`"hello world"`),
			want: "hello world",
		},
		{
			name: "array of text objects",
			in:   json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`),
			want: "ab",
		},
		{
			name: "unrecognised shape returns empty",
			in:   json.RawMessage(`12345`),
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractContentText(stream.Block{Content: tc.in})
			if got != tc.want {
				t.Errorf("extractContentText(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStripLineNumber covers each branch of the cat-n prefix stripper:
// a well-formed prefix is removed, content without the pattern passes
// through, and the leading-spaces/digits-only edge case (no tab) is
// preserved verbatim.
func TestStripLineNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"canonical cat -n prefix", "     1\thello", "hello"},
		{"large line number", "  1234\tlast", "last"},
		{"no tab keeps line intact", "  1234 not a prefix", "  1234 not a prefix"},
		{"no digits keeps line intact", "no number here", "no number here"},
		{"only spaces keeps line intact", "   ", "   "},
		{"empty string", "", ""},
		{"digits only no tab", "42", "42"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stripLineNumber(tc.in)
			if got != tc.want {
				t.Errorf("stripLineNumber(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEmitter_UserTextBlock exercises the emitUserText path, which is
// the kickoff prompt replay rendered as an output block. The body
// should reach the buffer line-by-line under the markerResult gutter.
func TestEmitter_UserTextBlock(t *testing.T) {
	e, buf, _ := newTestEmitter(t)
	e.OnUser(stream.User{Message: stream.Message{Content: []stream.Block{
		{Type: stream.BlockText, Text: "first line\nsecond line\n"},
	}}})

	got := buf.String()
	if !strings.Contains(got, "→  first line") {
		t.Errorf("expected leading marker on first user line, got:\n%s", got)
	}
	if !strings.Contains(got, "second line") {
		t.Errorf("expected continuation line in user text, got:\n%s", got)
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
