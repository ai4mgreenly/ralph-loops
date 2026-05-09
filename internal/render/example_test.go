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

func (nullRecorder) TallyBlock(string)         {}
func (nullRecorder) AddLLMTime(time.Duration)  {}
func (nullRecorder) AddToolTime(time.Duration) {}
func (nullRecorder) TrackUsage(*stream.Usage)  {}

// ExampleEmitter shows a single bash assistant event being rendered
// to a [bytes.Buffer]. The emitter's tool-call header lands on the
// first line in the canonical "←  <command>" shape; in colour mode
// chroma escapes would wrap each segment, but the non-colour theme
// keeps the // Output: block deterministic.
func ExampleEmitter() {
	var buf bytes.Buffer
	theme := ui.NewThemeWith(false, 0) // no color, no wrap
	em := render.NewEmitter(&buf, nullRecorder{}, theme)

	input, _ := json.Marshal(map[string]string{"command": "echo hello"})
	em.OnAssistant(stream.Assistant{
		Message: stream.Message{
			Role: "assistant",
			Content: []stream.Block{{
				Type:  stream.BlockToolUse,
				Name:  stream.ToolBash,
				ID:    "t1",
				Input: input,
			}},
		},
	})
	fmt.Print(buf.String())
	// Output: ←  echo hello
}
