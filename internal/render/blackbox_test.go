package render_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// blackboxRecorder satisfies [render.Recorder] from outside the
// package. The methods record only the hooks the tests assert on.
type blackboxRecorder struct {
	events map[string]int
	blocks map[string]int
	tools  []string
}

func (r *blackboxRecorder) TallyEvent(kind string)  { r.events[kind]++ }
func (r *blackboxRecorder) TallyBlock(t string)     { r.blocks[t]++ }
func (*blackboxRecorder) AddLLMTime(time.Duration)  {}
func (*blackboxRecorder) AddToolTime(time.Duration) {}
func (*blackboxRecorder) TrackMessageUsage(*stream.Usage, string, string, string) {
}
func (r *blackboxRecorder) TrackToolOutcome(name string, _ bool) {
	r.tools = append(r.tools, name)
}

func newBlackboxRecorder() *blackboxRecorder {
	return &blackboxRecorder{
		events: make(map[string]int),
		blocks: make(map[string]int),
	}
}

// TestEmitter_Blackbox_AssistantText exercises the most common
// rendering path — an assistant text message_end — through the public
// [render.NewEmitter] / [render.Emitter.OnEvent] API.
func TestEmitter_Blackbox_AssistantText(t *testing.T) {
	var buf bytes.Buffer
	rec := newBlackboxRecorder()
	em := render.NewEmitter(&buf, rec, ui.NewThemeWith(false, 0))

	em.OnEvent(stream.MessageEnd{Message: stream.PiMessage{
		Role:    stream.RoleAssistant,
		Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "hello world"}},
	}})

	if got := buf.String(); !strings.Contains(got, "hello world") {
		t.Errorf("expected text to reach output, got %q", got)
	}
	if rec.blocks[stream.BlockText] != 1 {
		t.Errorf("expected one text block tally, got %v", rec.blocks)
	}
	if rec.events[stream.TypeMessageEnd] != 1 {
		t.Errorf("expected one message_end event tally, got %v", rec.events)
	}
}

// TestEmitter_Blackbox_AssistantTextTruncates pins the operator-log
// guarantee that long assistant prose is capped at the configured
// output-line budget.
func TestEmitter_Blackbox_AssistantTextTruncates(t *testing.T) {
	var buf bytes.Buffer
	rec := newBlackboxRecorder()
	em := render.NewEmitter(&buf, rec, ui.NewThemeWith(false, 0),
		render.WithOutputLines(3))

	body := "line 1\nline 2\nline 3\nline 4\nline 5"
	em.OnEvent(stream.MessageEnd{Message: stream.PiMessage{
		Role:    stream.RoleAssistant,
		Content: []stream.ContentBlock{{Type: stream.BlockText, Text: body}},
	}})

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

// TestEmitter_Blackbox_ToolHeaderTruncates pins the bound on the
// B-lite tool-call header: a heredoc-style command carried as the
// call's `command` arg is whitespace-collapsed to one line and capped
// at the primary-arg rune limit, so a Write-via-heredoc call can't
// echo its whole body into the operator log.
func TestEmitter_Blackbox_ToolHeaderTruncates(t *testing.T) {
	var buf bytes.Buffer
	rec := newBlackboxRecorder()
	em := render.NewEmitter(&buf, rec, ui.NewThemeWith(false, 0))

	// > 60 runes after whitespace collapse, so the rune cap fires and
	// the trailing sentinel is elided behind an ellipsis.
	cmd := "cat > foo <<'EOF'\n" + strings.Repeat("PADDING ", 12) + "TAIL_SENTINEL\nEOF"
	args, _ := json.Marshal(map[string]string{"command": cmd})
	em.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "t1",
		ToolName:   "bash",
		Args:       args,
	})

	got := buf.String()
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis truncation in header, got %q", got)
	}
	if strings.Contains(got, "TAIL_SENTINEL") {
		t.Errorf("did not expect elided tail %q in header, got %q", "TAIL_SENTINEL", got)
	}
	// The header is a single collapsed line (no embedded newline before
	// the trailing block separator).
	header := strings.SplitN(got, "\n", 2)[0]
	if !strings.HasPrefix(header, "←  bash  cat > foo") {
		t.Errorf("expected collapsed single-line header, got %q", header)
	}
}

// TestEmitter_Blackbox_EditDiff drives the locked edit-diff path from
// outside the package: a toolName=="edit" start whose Args carries the
// real pi shape produces a `-/+` diff on the matching end.
func TestEmitter_Blackbox_EditDiff(t *testing.T) {
	var buf bytes.Buffer
	rec := newBlackboxRecorder()
	em := render.NewEmitter(&buf, rec, ui.NewThemeWith(false, 0))

	em.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "call_edit",
		ToolName:   "edit",
		Args:       json.RawMessage(`{"path":"edit-target.txt","edits":[{"oldText":"quick","newText":"slow"}]}`),
	})
	em.OnEvent(stream.ToolExecutionEnd{
		ToolCallID: "call_edit",
		ToolName:   "edit",
		Result:     json.RawMessage(`{"content":[{"type":"text","text":"Successfully replaced 1 block(s)."}]}`),
	})

	got := buf.String()
	if !strings.Contains(got, "←  edit  edit-target.txt") {
		t.Errorf("expected B-lite edit header, got %q", got)
	}
	if !strings.Contains(got, "- quick") || !strings.Contains(got, "+ slow") {
		t.Errorf("expected reconstructed diff lines, got %q", got)
	}
	if rec.events[stream.TypeToolExecutionStart] != 1 || rec.events[stream.TypeToolExecutionEnd] != 1 {
		t.Errorf("tool events not tallied by kind: %v", rec.events)
	}
	if len(rec.tools) != 1 || rec.tools[0] != "edit" {
		t.Errorf("tool outcome not recorded: %v", rec.tools)
	}
}
