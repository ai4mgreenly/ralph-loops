package render

import (
	"encoding/json"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// emitEditResult renders the result of an Edit tool call as a
// line-by-line diff between the call's old_string and new_string:
// removed lines get a `- ` prefix and a dim-red background tint,
// additions get `+ ` and a dim-green background tint, and surrounding
// context (lines common to both) get two leading spaces and no tint.
// Each line body is syntax-highlighted using a lexer chosen from the
// original Edit call's `file_path`; the bg-restoration in
// [ui.Color.Paint] keeps the diff tint solid across chroma's
// per-token resets. The diff is computed from the saved tool-call
// input rather than the result block, so the diff reflects what the
// agent intended to change even when the edit failed.
func (e *Emitter) emitEditResult(b stream.Block, ref toolRef) {
	marker := markerResult
	if b.IsError {
		marker = markerError
	}

	var input struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	_ = json.Unmarshal(ref.input, &input)

	filePath := filePathOf(ref.input)
	highlightedOld := indexHighlightedLines(filePath, input.OldString, e.theme.UseColor())
	highlightedNew := indexHighlightedLines(filePath, input.NewString, e.theme.UseColor())

	d := diffLines(input.OldString, input.NewString)
	lines := make([]ui.Line, 0, len(d))
	var oi, ni int
	for _, op := range d {
		var prefix string
		var color ui.Color
		var body string
		switch op.op {
		case diffRemove:
			prefix, color = "- ", ui.DiffRemoveBg
			body = pickHighlighted(highlightedOld, oi, op.text)
			oi++
		case diffAdd:
			prefix, color = "+ ", ui.DiffAddBg
			body = pickHighlighted(highlightedNew, ni, op.text)
			ni++
		default:
			prefix, color = "  ", ui.Plain
			body = pickHighlighted(highlightedNew, ni, op.text)
			oi++
			ni++
		}
		lines = append(lines, ui.Line{Text: prefix + body, Color: color})
	}

	e.emitOutputBlock(marker, lines)
}

// indexHighlightedLines returns the lines of content highlighted for
// filePath, or nil if highlighting failed in a way that left the line
// count out of sync with the source. Callers pair the returned slice
// with the original lines so a fall-through to plain text is always
// possible per line.
func indexHighlightedLines(filePath, content string, useColor bool) []string {
	if content == "" {
		return nil
	}
	plain := splitDiffLines(content)
	hi := highlightLines(filePath, content, useColor)
	if len(hi) != len(plain) {
		return plain
	}
	return hi
}

// pickHighlighted returns lines[i] when in range, otherwise fallback.
// Used by the Edit and Write renderers as a per-line fallback so a
// short or nil highlighted slice never panics the renderer — it just
// degrades to the raw source text for that line.
func pickHighlighted(lines []string, i int, fallback string) string {
	if i < 0 || i >= len(lines) {
		return fallback
	}
	return lines[i]
}
