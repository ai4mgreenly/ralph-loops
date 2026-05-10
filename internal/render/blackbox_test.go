package render_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// blackboxRecorder satisfies [render.Recorder] from outside the
// package. The methods record only the hooks under test.
type blackboxRecorder struct {
	blocks   map[string]int
	llmTime  time.Duration
	toolTime time.Duration
}

func (r *blackboxRecorder) TallyBlock(t string)         { r.blocks[t]++ }
func (r *blackboxRecorder) AddLLMTime(d time.Duration)  { r.llmTime += d }
func (r *blackboxRecorder) AddToolTime(d time.Duration) { r.toolTime += d }
func (*blackboxRecorder) TrackUsage(*stream.Usage)      {}

// TestEmitter_Blackbox_AssistantText exercises the most common
// rendering path — an assistant text block — through the public
// [render.NewEmitter] / [render.Emitter.OnAssistant] API. The
// emitter writes a `←` lead line followed by an empty separator;
// this is the exact contract operators see in production.
func TestEmitter_Blackbox_AssistantText(t *testing.T) {
	var buf bytes.Buffer
	rec := &blackboxRecorder{blocks: make(map[string]int)}
	em := render.NewEmitter(&buf, rec, ui.NewThemeWith(false, 0))

	em.OnAssistant(stream.Assistant{
		Message: stream.Message{
			Role:    "assistant",
			Content: []stream.Block{{Type: stream.BlockText, Text: "hello world"}},
		},
	})

	if got := buf.String(); !strings.Contains(got, "hello world") {
		t.Errorf("expected text to reach output, got %q", got)
	}
	if rec.blocks[stream.BlockText] != 1 {
		t.Errorf("expected one text block tally, got %v", rec.blocks)
	}
}

// TestEmitter_Blackbox_AssistantTextTruncates pins the operator-log
// guarantee that long assistant prose is capped at the configured
// output-line budget. Without the cap, a multi-paragraph narration
// (or any block containing more than --output-lines lines) would dump
// in full and drown the surrounding tool-call/result pairs.
func TestEmitter_Blackbox_AssistantTextTruncates(t *testing.T) {
	var buf bytes.Buffer
	rec := &blackboxRecorder{blocks: make(map[string]int)}
	em := render.NewEmitter(&buf, rec, ui.NewThemeWith(false, 0),
		render.WithOutputLines(3))

	body := "line 1\nline 2\nline 3\nline 4\nline 5"
	em.OnAssistant(stream.Assistant{
		Message: stream.Message{
			Role:    "assistant",
			Content: []stream.Block{{Type: stream.BlockText, Text: body}},
		},
	})

	got := buf.String()
	for _, want := range []string{"line 1", "line 2", "line 3", "..."} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got %q", want, got)
		}
	}
	for _, unwanted := range []string{"line 4", "line 5"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("did not expect %q in output, got %q", unwanted, got)
		}
	}
}

// TestEmitter_Blackbox_BashCallTruncates pins the same cap on the
// Bash tool-call header. Heredoc-style commands (e.g. `cat > foo
// <<'EOF' ... EOF`) carry the entire payload inline as the call's
// `command` field; without truncation a single Write-via-heredoc call
// would echo the whole file body into the operator log.
func TestEmitter_Blackbox_BashCallTruncates(t *testing.T) {
	var buf bytes.Buffer
	rec := &blackboxRecorder{blocks: make(map[string]int)}
	em := render.NewEmitter(&buf, rec, ui.NewThemeWith(false, 0),
		render.WithOutputLines(2))

	cmd := `cat > foo <<'EOF'
ALPHA
BRAVO
CHARLIE
DELTA
EOF`
	em.OnAssistant(stream.Assistant{
		Message: stream.Message{
			Role: "assistant",
			Content: []stream.Block{{
				Type:  stream.BlockToolUse,
				ID:    "tool_1",
				Name:  stream.ToolBash,
				Input: []byte(`{"command":` + jsonString(cmd) + `}`),
			}},
		},
	})

	got := buf.String()
	if !strings.Contains(got, "...") {
		t.Errorf("expected truncation marker `...` in output, got %q", got)
	}
	// First two lines (`cat > foo <<'EOF'` and `ALPHA`) should appear;
	// later content must be elided. ("EOF" is intentionally not in the
	// negative list because it's a substring of the visible opener line.)
	for _, unwanted := range []string{"CHARLIE", "DELTA"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("did not expect %q in truncated output, got %q", unwanted, got)
		}
	}
}

// jsonString returns s as a JSON string literal: `"...\n..."` with
// embedded newlines escaped. Avoids pulling encoding/json into the
// test for one inline payload.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TestEmitter_Blackbox_DecodeStatus verifies the small public
// helper [render.DecodeStatus] from outside the package: it pulls
// the status field out of the result event's structured output.
func TestEmitter_Blackbox_DecodeStatus(t *testing.T) {
	got := render.DecodeStatus([]byte(`{"status":"DONE"}`))
	if got != "DONE" {
		t.Errorf("DecodeStatus = %q, want DONE", got)
	}
	if got := render.DecodeStatus(nil); got != "" {
		t.Errorf("DecodeStatus(nil) = %q, want empty", got)
	}
}
