package render

import (
	"encoding/json"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// emitWriteResult renders the result of a Write tool call as a
// "diff from nothing": every line of the new content is shown as an
// addition (`+ ` prefix, green background tint), mirroring the way
// Edit renders its hunks. Line bodies are syntax-highlighted from
// the original Write call's `file_path`. This is faithful for
// new-file writes (the common case) and a stylised view for
// overwrites — the `←  Write  <path>` header is the operator's cue
// that something pre-existing may have been replaced.
func (e *Emitter) emitWriteResult(b stream.Block, ref toolRef) {
	marker := markerResult
	if b.IsError {
		marker = markerError
	}

	content := strings.TrimRight(writeContent(ref.input), "\n")
	if content == "" {
		e.emitOutputBlock(marker, nil)
		return
	}

	plain := strings.Split(content, "\n")
	hi := indexHighlightedLines(filePathOf(ref.input), content, e.theme.UseColor())

	lines := make([]ui.Line, len(plain))
	for i, raw := range plain {
		body := pickHighlighted(hi, i, strings.TrimRight(raw, " \t\r"))
		lines[i] = ui.Line{Text: "+ " + body, Color: ui.DiffAddBg}
	}
	e.emitOutputBlock(marker, lines)
}

// writeContent pulls the `content` field from a Write tool_use
// input. Same fail-soft semantics as [bashCommand].
func writeContent(input json.RawMessage) string {
	var s struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(input, &s)
	return s.Content
}
