package render

import (
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// highlightStyle is the chroma style applied to every Read, Write, and
// Edit render. Picked once for visual consistency; tweak here to
// retheme.
const highlightStyle = "monokai"

// highlightLines tokenises content with a lexer chosen from filePath
// and returns one line per element with terminal SGR escapes embedded
// for token colours. Falls back to the input split on `\n` when colour
// is disabled, no lexer matches, or any chroma step errors — the
// renderer gets uncoloured text and life goes on.
//
// Each returned line is self-contained: any SGR span that crossed a
// `\n` in chroma's raw output is closed at the line break and reopened
// at the start of the next line, so [ui.Theme.WriteBlock]'s per-line
// painting and wrapping never run into half-open spans.
func highlightLines(filePath, content string, useColor bool) []string {
	plain := splitLinesNoTrailing(content)
	if !useColor || filePath == "" {
		return plain
	}
	lexer := lexers.Match(filePath)
	if lexer == nil {
		return plain
	}
	style := styles.Get(highlightStyle)
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return plain
	}
	iter, err := lexer.Tokenise(nil, content)
	if err != nil {
		return plain
	}
	var sb strings.Builder
	if err := formatter.Format(&sb, style, iter); err != nil {
		return plain
	}
	return balanceSGRPerLine(splitLinesNoTrailing(sb.String()))
}

// splitLinesNoTrailing splits s on `\n` and drops a single trailing
// empty element if s ends with a newline, so a "foo\nbar\n" input
// renders as two lines, not three.
func splitLinesNoTrailing(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

// balanceSGRPerLine walks lines in order, tracking SGR escapes that
// were opened on a previous line and not yet reset, and:
//
//   - prepends those still-open escapes to the current line so the
//     colour resumes correctly when each line is rendered standalone;
//   - appends a single `\x1b[0m` at the line end if the line itself
//     leaves any SGR open, so the line never trails colour into
//     whatever the renderer prints next.
//
// Most chroma terminal output is already line-balanced — this is a
// safety net for languages whose lexers emit multi-line spans (block
// strings, doc comments, etc.).
func balanceSGRPerLine(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	var carryIn []string
	for i, raw := range lines {
		var sb strings.Builder
		for _, esc := range carryIn {
			sb.WriteString(esc)
		}
		sb.WriteString(raw)
		open := append([]string(nil), carryIn...)
		for _, tok := range scanSGR(raw) {
			if isReset(tok) {
				open = open[:0]
			} else {
				open = append(open, tok)
			}
		}
		if len(open) > 0 {
			sb.WriteString("\x1b[0m")
		}
		out[i] = sb.String()
		carryIn = open
	}
	return out
}

// scanSGR returns the CSI escape sequences in s in order, ignoring
// non-escape text. Used by [balanceSGRPerLine] to track open spans
// across line boundaries.
func scanSGR(s string) []string {
	if !strings.ContainsRune(s, 0x1b) {
		return nil
	}
	var out []string
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
			out = append(out, s[i:j])
			i = j
			continue
		}
		i++
	}
	return out
}

// isReset reports whether tok is a full SGR reset (`\x1b[0m` or the
// parameter-omitted `\x1b[m`).
func isReset(tok string) bool {
	return tok == "\x1b[0m" || tok == "\x1b[m"
}
