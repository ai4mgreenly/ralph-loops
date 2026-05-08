package ui

import (
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Theme is the explicitly constructed handle that replaces the
// package-level globals previously used to thread colour and width
// state through the rendering layer. Construct one with [NewTheme]
// (or [NewThemeWith] in tests) and pass it to whichever consumer
// renders human-facing output.
//
// Theme is safe for concurrent use: the only mutable field, the
// terminal width, is held in an [atomic.Int32] so the SIGWINCH
// watcher can update it while [Theme.Width] is read from rendering
// goroutines.
type Theme struct {
	useColor bool
	width    atomic.Int32
}

// NewTheme constructs a Theme by inspecting out: it queries the
// controlling terminal for column count and disables colour when
// either NO_COLOR is set or out is not a character device. Importing
// the ui package has no side effects — colour and width state lives
// only on the Theme value the caller constructs and threads through.
func NewTheme(out *os.File) *Theme {
	t := &Theme{useColor: detectColor(out)}
	t.width.Store(int32(detectWidth(out)))
	return t
}

// NewThemeWith constructs a Theme directly from explicit values. It
// is intended for tests that want deterministic colour and width
// settings without touching a real terminal.
func NewThemeWith(useColor bool, width int) *Theme {
	t := &Theme{useColor: useColor}
	t.width.Store(int32(width))
	return t
}

// UseColor reports whether the theme will emit ANSI escapes.
func (t *Theme) UseColor() bool {
	if t == nil {
		return false
	}
	return t.useColor
}

// Width returns the current terminal column count, or 0 when the
// terminal is unknown (output piped, not a TTY, query failed). The
// value may be updated concurrently by the SIGWINCH watcher started
// by [Theme.WatchResize], so callers see the latest detected width.
func (t *Theme) Width() int {
	if t == nil {
		return 0
	}
	return int(t.width.Load())
}

// RuleFallbackWidth is the rule width used when the terminal width is
// unknown (output piped, NO_TERM, etc).
const RuleFallbackWidth = 70

// RuleChar is the unicode horizontal rule character used to bracket
// section panels.
const RuleChar = "─"

// BuildRule returns a horizontal rule sized to width, falling back
// to [RuleFallbackWidth] when width is non-positive (e.g. stdout is
// piped).
func BuildRule(width int) string {
	if width <= 0 {
		width = RuleFallbackWidth
	}
	return strings.Repeat(RuleChar, width)
}

// Rule returns a horizontal rule sized to the theme's current
// terminal width, falling back to [RuleFallbackWidth] when the width
// is unknown (e.g. stdout is piped). Used by the iteration banner and
// the closing summary panel.
func (t *Theme) Rule() string {
	return BuildRule(t.Width())
}

// SetWidth overrides the stored width. Used by the SIGWINCH watcher
// and by tests; production callers normally rely on the value
// detected by [NewTheme].
func (t *Theme) SetWidth(w int) {
	if t == nil {
		return
	}
	t.width.Store(int32(w))
}

// detectColor returns true when ANSI colour output is appropriate. It
// suppresses colour when NO_COLOR is set (per no-color.org) and when w
// is not a character device (i.e. output is being captured to a file
// or piped to another program).
func detectColor(w *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := w.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// detectWidth queries the controlling terminal's column count via
// [golang.org/x/term.GetSize]. Returns 0 when w is not a character
// device or the query fails, signalling "do not truncate".
func detectWidth(w *os.File) int {
	fi, err := w.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return 0
	}
	cols, _, err := term.GetSize(int(w.Fd()))
	if err != nil || cols <= 0 {
		return 0
	}
	return cols
}

// Paint wraps s in colour's ANSI escape pair when this theme has
// colour enabled. Empty strings pass through untouched and [Plain] is
// a no-op.
//
// Background colours ([ToolCallBg], [DiffAddBg], [DiffRemoveBg]) also
// re-emit their background escape after every interior `\x1b[0m` in
// s, so a foreground highlighter (e.g. chroma) whose token resets
// clear the background still keeps the bg fill solid across the full
// line.
func (t *Theme) Paint(c Color, s string) string {
	if t == nil || !t.useColor || s == "" {
		return s
	}
	switch c {
	case Dim:
		return dimEscape + s + resetEscape
	case DimRed:
		return dimRedEscape + s + resetEscape
	case DimGreen:
		return dimGreenEscape + s + resetEscape
	case LightBlue:
		return lightBlueEscape + s + resetEscape
	case Orange:
		return orangeEscape + s + resetEscape
	case Bright:
		return brightEscape + s + resetEscape
	case ToolCallBg:
		return toolCallBgEscape + restoreBg(s, toolCallBgEscape) + resetEscape
	case DiffAddBg:
		return diffAddBgEscape + restoreBg(s, diffAddBgEscape) + resetEscape
	case DiffRemoveBg:
		return diffRemoveBgEscape + restoreBg(s, diffRemoveBgEscape) + resetEscape
	}
	return s
}

// WriteBlock renders marker + lines as a single decorated block:
//
//   - lines[0]'s first visible segment is prefixed with marker
//     right-padded to [Gutter] columns;
//   - every other visible segment — later input lines and soft-wrap
//     continuations alike — is prefixed with [Gutter] spaces, giving
//     the whole block one clean left edge;
//   - each segment is painted in its line's [Color] after wrapping so
//     ANSI escapes do not count toward the wrap budget;
//   - long lines wrap at [Theme.Width] - [Gutter]; on a non-TTY
//     ([Theme.Width] returns 0) lines pass through verbatim;
//   - a single trailing newline terminates the last line; trailingBlank
//     adds one more newline after, producing a blank separator.
//
// Callers with no content (lines == nil) get a bare marker line — the
// helper never emits an empty block.
func (t *Theme) WriteBlock(w io.Writer, marker string, lines []Line, trailingBlank bool) {
	avail := 0
	if width := t.Width(); width > 0 {
		avail = width - Gutter
	}
	cont := gutterSpaces()
	first := markerPrefix(marker)

	if len(lines) == 0 {
		writeString(w, strings.TrimRight(first, " ")+"\n")
		if trailingBlank {
			writeString(w, "\n")
		}
		return
	}

	isFirst := true
	for _, line := range lines {
		segs := wrapVisible(expandTabs(line.Text), avail)
		fill := line.Color.fillsLine() && avail > 0
		for _, seg := range segs {
			prefix := cont
			if isFirst {
				prefix = first
				isFirst = false
			}
			body := seg
			if fill {
				body = padToWidth(body, avail)
			}
			writeString(w, prefix+t.Paint(line.Color, body)+"\n")
		}
	}
	if trailingBlank {
		writeString(w, "\n")
	}
}

// Decorate writes one logical line — possibly soft-wrapped onto
// several visible lines — prefixed with marker padded to [Gutter]
// columns, painted in dim grey, followed by a blank separator. It is
// the standard treatment for every non-assistant-text status line
// ralph prints.
func (t *Theme) Decorate(w io.Writer, marker, text string) {
	t.WriteBlock(w, marker, []Line{{Text: text, Color: Dim}}, true)
}

// Raw writes a no-colour multi-line block. The first input line gets
// marker padded to [Gutter] columns; later input lines and soft-wrap
// continuations get [Gutter] spaces. Trailing whitespace on each line
// is trimmed; an empty body produces a bare marker line. Unlike
// [Theme.Decorate] no trailing blank is emitted — the caller decides
// whether the block terminates a pair.
func (t *Theme) Raw(w io.Writer, marker, text string) {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		t.WriteBlock(w, marker, nil, false)
		return
	}
	parts := strings.Split(text, "\n")
	lines := make([]Line, len(parts))
	for i, l := range parts {
		lines[i] = Line{Text: strings.TrimRight(l, " \t\r")}
	}
	t.WriteBlock(w, marker, lines, false)
}

// Tool writes a tool-call header block. Same gutter and wrap shape as
// [Theme.Raw] / [Theme.Lead], but every visible line is painted with
// the subdued [ToolCallBg] fill so the operator can see at a glance
// where a tool call begins and how far its content stretches. The
// background fills the entire post-gutter content area on every line
// — short content is right-padded to the terminal edge inside the
// background escape; the gutter itself is never coloured.
//
// trailingBlank controls whether a blank separator follows: tool
// calls whose results render as a tucked-under output block (Bash,
// Read, Edit, Write) pass false so the result `→` sits flush against
// the call; tools whose results are summarised on a single `→` line
// pass true so the call/result pair has its own breathing room.
func (t *Theme) Tool(w io.Writer, marker, text string, trailingBlank bool) {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		t.WriteBlock(w, marker, nil, trailingBlank)
		return
	}
	parts := strings.Split(text, "\n")
	lines := make([]Line, len(parts))
	for i, l := range parts {
		lines[i] = Line{Text: strings.TrimRight(l, " \t\r"), Color: ToolCallBg}
	}
	t.WriteBlock(w, marker, lines, trailingBlank)
}

// Lead writes a prose block in the same gutter shape as [Theme.Raw]
// but with a trailing blank separator and a light-blue foreground
// tint. Used for assistant text — the colour distinguishes the
// model's prose from tool calls, results, and status lines at a
// glance. Empty bodies emit nothing.
func (t *Theme) Lead(w io.Writer, marker, text string) {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		return
	}
	parts := strings.Split(text, "\n")
	lines := make([]Line, len(parts))
	for i, l := range parts {
		lines[i] = Line{Text: strings.TrimRight(l, " \t\r"), Color: LightBlue}
	}
	t.WriteBlock(w, marker, lines, true)
}

// padToWidth right-pads s with spaces so its display width equals
// width. Strings already at or above width pass through untouched.
// Used by [Theme.WriteBlock] to fill background-coloured segments out
// to the right edge of the terminal. ANSI escape sequences embedded
// in s do not count toward the visible width; widths are measured by
// [ansi.StringWidth] so wide chars and zero-width joiners are handled
// correctly.
func padToWidth(s string, width int) string {
	n := ansi.StringWidth(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// writeString is a tiny helper that swallows the io.Writer error —
// callers in this package print to a terminal or buffer where a
// failed write means the operator has bigger problems than a stale
// status line.
func writeString(w io.Writer, s string) {
	_, _ = io.WriteString(w, s)
}
