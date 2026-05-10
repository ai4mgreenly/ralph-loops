package render

import (
	"encoding/json"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// emitBashResult renders the result of a Bash tool call as a tight
// terminal-style block: stdout lines first, stderr lines after in dim
// red, capped at [Emitter.OutputLines] with a trailing `...` when
// more were available.
//
// The Anthropic claude CLI ships a parsed `tool_use_result` sidecar on
// the user event with split stdout/stderr fields; when that is present
// we keep stderr dim-red. Other engines (e.g. ikigai-cli driving
// Google) emit no sidecar — the joined output, including any
// engine-appended "[exit: N]" trailer, lives in the tool_result
// block's `content` field. In that branch we render every line in
// [ui.Plain] since the wire format does not preserve the stdout/stderr
// boundary.
func (e *Emitter) emitBashResult(b stream.Block, structured json.RawMessage) {
	marker := markerResult
	if b.IsError {
		marker = markerError
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

	if len(structured) > 0 {
		var s struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		_ = json.Unmarshal(structured, &s)
		push(s.Stdout, ui.Plain)
		push(s.Stderr, ui.DimRed)
	} else {
		push(extractContentText(b), ui.Plain)
	}

	e.emitOutputBlock(marker, lines)
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
