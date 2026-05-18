package render_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// nullRecorder is a no-op [render.Recorder]. The emitter calls into a
// recorder for stats accounting; the example doesn't assert on those
// values, so we discard the events.
type nullRecorder struct{}

func (nullRecorder) TallyEvent(string)         {}
func (nullRecorder) TallyBlock(string)         {}
func (nullRecorder) AddLLMTime(time.Duration)  {}
func (nullRecorder) AddToolTime(time.Duration) {}
func (nullRecorder) TrackMessageUsage(*stream.Usage, string, string, string) {
}
func (nullRecorder) TrackToolOutcome(string, bool) {}

// ExampleEmitter shows a single bash tool-execution start being
// rendered to a [bytes.Buffer]. The B-lite header lands on the first
// line in the canonical "←  <toolName>  <primary arg>" shape; the
// non-colour theme keeps the // Output: block deterministic.
func ExampleEmitter() {
	var buf bytes.Buffer
	theme := ui.NewThemeWith(false, 0) // no color, no wrap
	em := render.NewEmitter(&buf, nullRecorder{}, theme)

	args, _ := json.Marshal(map[string]string{"command": "echo hello"})
	em.OnEvent(stream.ToolExecutionStart{
		ToolCallID: "t1",
		ToolName:   "bash",
		Args:       args,
	})
	fmt.Print(buf.String())
	// Output: ←  bash  echo hello
}
