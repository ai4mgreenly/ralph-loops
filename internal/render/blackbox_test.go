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
