package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// emitReadResult renders the result of a Read tool call as the same
// terminal-style block used by Bash, but populated from the file
// content claude returns in the result block. The result text comes
// back with `cat -n`-style line-number prefixes; those are stripped
// so what the operator sees matches what the agent saw, then the
// stripped body is run through the syntax highlighter using the
// original Read call's `file_path` to pick a lexer.
func (e *Emitter) emitReadResult(b stream.Block, ref toolRef) {
	marker := markerResult
	if b.IsError {
		marker = markerError
	}

	content := strings.TrimRight(extractContentText(b), "\n")
	if content == "" {
		e.emitOutputBlock(marker, nil)
		return
	}

	stripped := make([]string, 0, strings.Count(content, "\n")+1)
	for _, l := range strings.Split(content, "\n") {
		stripped = append(stripped, strings.TrimRight(stripLineNumber(l), " \t\r"))
	}
	// Drop a single trailing empty element so the line count matches
	// what chroma's output produces after [splitLinesNoTrailing] strips
	// its own trailing newline. Without this, files whose last source
	// line is blank (or whose content ends in `\n` once stripped of line
	// numbers) trigger the length-mismatch fallback to plain text.
	if n := len(stripped); n > 0 && stripped[n-1] == "" {
		stripped = stripped[:n-1]
	}

	highlighted := highlightLines(filePathOf(ref.input), strings.Join(stripped, "\n"), e.theme.UseColor())
	if len(highlighted) != len(stripped) {
		highlighted = stripped
	}

	lines := make([]ui.Line, len(highlighted))
	for i, text := range highlighted {
		lines[i] = ui.Line{Text: text}
	}

	e.emitOutputBlock(marker, lines)
}

// extractContentText pulls plain text out of a tool_result block's
// `content` field, which the claude CLI encodes either as a bare JSON
// string or as an array of {type:"text", text:"..."} objects. Returns
// an empty string for any other shape.
func extractContentText(b stream.Block) string {
	if len(b.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		return s
	}
	var arr []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b.Content, &arr); err == nil {
		var sb strings.Builder
		for _, item := range arr {
			sb.WriteString(item.Text)
		}
		return sb.String()
	}
	return ""
}

// stripLineNumber removes a leading `cat -n`-style line-number prefix
// (optional spaces, digits, single tab) from s. Lines that don't match
// the pattern pass through unchanged, so non-Read consumers and any
// content that legitimately starts with whitespace and digits stay
// intact.
func stripLineNumber(s string) string {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	digitStart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == digitStart || i >= len(s) || s[i] != '\t' {
		return s
	}
	return s[i+1:]
}

// readTarget renders a Read tool_use input as a `file_path` plus an
// optional `:start-end` line-range suffix when the agent narrowed the
// read via `offset` and/or `limit`. The suffix mirrors grep -n's
// `file:line` convention so the operator can see at a glance what
// slice the agent asked for.
//
//	Read(foo.go)                       → "foo.go"
//	Read(foo.go, offset=200, limit=50) → "foo.go:200-249"
//	Read(foo.go, offset=200)           → "foo.go:200-"
//	Read(foo.go, limit=50)             → "foo.go:1-50"
//
// Same fail-soft semantics as [filePathOf].
func readTarget(input json.RawMessage) string {
	var s struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	_ = json.Unmarshal(input, &s)
	if s.Offset == 0 && s.Limit == 0 {
		return s.FilePath
	}
	start := s.Offset
	if start == 0 {
		start = 1
	}
	if s.Limit > 0 {
		return fmt.Sprintf("%s:%d-%d", s.FilePath, start, start+s.Limit-1)
	}
	return fmt.Sprintf("%s:%d-", s.FilePath, start)
}
