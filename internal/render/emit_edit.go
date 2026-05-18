package render

import (
	"encoding/json"

	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// editArgs is the verified pi v0.75.0 `edit` tool argument shape:
// a target path plus an ARRAY of replacement blocks. Each block is an
// independent oldText→newText substitution; the diff is reconstructed
// per block in array order (NOT a flat args.oldText/newText pair).
type editArgs struct {
	Path  string `json:"path"`
	Edits []struct {
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	} `json:"edits"`
}

// emitEditDiff renders an `edit` tool execution as a reconstructed,
// colorized diff. The locked decision is to rebuild the diff from the
// tool_execution_start args' edits[] through the existing
// engine-agnostic diff+highlight code rather than trust the optional
// ready-made result.details.diff: removed lines get a `- ` prefix and a
// dim-red background tint, additions get `+ ` and a dim-green tint, and
// surrounding context gets two leading spaces and no tint. Line bodies
// are syntax-highlighted using a lexer chosen from the edit's `path`.
// Every edits[] block is diffed in order and concatenated into one
// block so a multi-hunk edit reads as a single change set.
//
// args is the raw tool_execution_start args captured in the pending
// ledger; an absent or unparseable args (the matching start was never
// seen, e.g. a truncated stream) yields nothing rather than a panic.
func (e *Emitter) emitEditDiff(marker string, args []byte) {
	if len(args) == 0 {
		return
	}
	var a editArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return
	}

	var lines []ui.Line
	for _, ed := range a.Edits {
		highlightedOld := indexHighlightedLines(a.Path, ed.OldText, e.theme.UseColor())
		highlightedNew := indexHighlightedLines(a.Path, ed.NewText, e.theme.UseColor())

		d := diffLines(ed.OldText, ed.NewText)
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
	}

	e.emitOutputBlock(marker, lines)
}

// indexHighlightedLines returns the lines of content highlighted for
// filePath, or the plain split when highlighting left the line count
// out of sync with the source. Callers pair the returned slice with
// the original lines so a fall-through to plain text is always possible
// per line.
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
// Used by the edit-diff renderer as a per-line fallback so a short or
// nil highlighted slice never panics — it just degrades to the raw
// source text for that line.
func pickHighlighted(lines []string, i int, fallback string) string {
	if i < 0 || i >= len(lines) {
		return fallback
	}
	return lines[i]
}
