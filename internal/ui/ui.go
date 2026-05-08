// Package ui contains the human-facing output helpers used by the
// iteration driver: decorated status lines, byte and time formatters,
// and number formatting with thousands separators.
//
// All output goes to os.Stdout. ANSI colour escapes are emitted only
// when stdout is a terminal and the NO_COLOR environment variable is
// unset, in line with https://no-color.org. The functions here are
// deliberately simple — ralph is a CLI driver, not a TUI — and stay
// free of any dependency on the loop or stream packages so they can be
// unit-tested in isolation.
package ui

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// tabWidth is the number of spaces used to expand a `\t` character
// before any width calculation. Tabs cannot be measured reliably —
// terminals advance them to a column-modulo tab stop, while
// [ansi.StringWidth] reports them as zero-width — so block content
// is normalised to spaces at the entry to [WriteBlock] and every
// downstream wrap, pad, and paint operation sees a tab-free string.
// Four matches the convention most source files use; the value is
// intentionally not configurable.
const tabWidth = 4

// expandTabs replaces every `\t` in s with [tabWidth] spaces. The
// substitution is a flat replace rather than column-aware tab-stop
// expansion because block content in ralph is almost always
// indentation, where a fixed expansion gives identical visual output
// to a real tab stop.
func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	return strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))
}

// Gutter is the left-margin width every decorated block reserves for the
// first-line marker and continuation prefixes. The first visible line of a
// block is prefixed with the marker right-padded to Gutter columns; every
// other visible line — explicit `\n` continuations and soft-wrap
// continuations alike — is prefixed with Gutter spaces, so the entire block
// shares one clean left edge.
const Gutter = 3

// ANSI escape sequences. dimEscape paints a "decorated" informational
// line in dim grey; dimRedEscape and dimGreenEscape paint diff and
// error output (stderr, removed lines / added lines) without the eye
// strain of full-saturation red and green. toolCallBgEscape paints a
// subdued background fill on tool-call header lines so the operator
// can see at a glance where one tool call ends and the next begins;
// it uses the 256-colour palette (index 236, very dark grey) rather
// than the 16-colour basic set so it stays subtle on dark terminals.
const (
	dimEscape          = "\x1b[90m"
	dimRedEscape       = "\x1b[2;31m"
	dimGreenEscape     = "\x1b[2;32m"
	lightBlueEscape    = "\x1b[38;5;111m"
	orangeEscape       = "\x1b[38;5;208m"
	brightEscape       = "\x1b[97m"
	toolCallBgEscape   = "\x1b[48;5;238m"
	diffAddBgEscape    = "\x1b[48;2;30;50;30m"
	diffRemoveBgEscape = "\x1b[48;2;60;30;30m"
	resetEscape        = "\x1b[0m"
)

// useColor decides at startup whether the ui helpers emit ANSI escapes.
// Tests can flip it via [SetColor]. Stored as an [atomic.Bool] because
// it is read concurrently from the spinner goroutine and any goroutine
// that calls [Color.Paint], while [SetColor] may be called from another
// goroutine (notably tests running in parallel).
var useColor atomic.Bool

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

// SetColor overrides the auto-detected colour setting. Intended for
// tests; production code never needs it.
func SetColor(on bool) { useColor.Store(on) }

// UseColor reports whether the package will emit ANSI escapes. Callers
// that produce their own escape sequences (e.g. a syntax highlighter)
// gate their output on this so NO_COLOR and non-TTY runs still come
// out clean.
func UseColor() bool { return useColor.Load() }

// terminalWidth is the detected stdout column count, or 0 when stdout
// is not a TTY (in which case callers should leave output untruncated
// — they're probably writing to a log). It is updated on SIGWINCH by
// the goroutine [WatchResize] starts, so reads and writes are
// synchronised through atomic ops.
var terminalWidth atomic.Int32

func init() {
	useColor.Store(detectColor(os.Stdout))
	terminalWidth.Store(int32(detectWidth(os.Stdout)))
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

// TerminalWidth returns the detected terminal column count, or 0 when
// stdout is not a TTY. Callers truncating output should treat 0 as
// "leave the line alone".
func TerminalWidth() int { return int(terminalWidth.Load()) }

// SetTerminalWidth overrides the detected width. Intended for tests
// that exercise truncation behaviour.
func SetTerminalWidth(n int) { terminalWidth.Store(int32(n)) }

// WatchResize installs a SIGWINCH handler that re-detects the
// terminal width whenever the controlling terminal is resized.
// Subsequent calls to [TerminalWidth] (and the wrap math driven from
// it) observe the new value, so output rendered after the resize
// honours the new width. Output already on screen is not redrawn.
//
// The returned function uninstalls the handler and stops the
// background goroutine; callers should defer it.
func WatchResize() func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sigCh:
				terminalWidth.Store(int32(detectWidth(os.Stdout)))
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}

// Truncate shortens s to at most max runes, replacing the final rune
// with a horizontal ellipsis when truncation occurs. max <= 0 returns
// s unchanged so callers can pass through "width unknown" without a
// branch at every call site.
func Truncate(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// Color paints a visible segment after wrapping. Used by [WriteBlock]
// so ANSI escape sequences are not counted toward the wrap budget.
//
// Most colours are foreground tints applied to the segment text only.
// [ToolCallBg] is special: it paints a subdued background fill across
// the entire post-gutter content area, padding short segments out to
// the available width so the background extends to the right edge of
// the terminal on every wrapped line.
type Color int

const (
	Plain Color = iota
	Dim
	DimRed
	DimGreen
	LightBlue
	Orange
	Bright
	ToolCallBg
	DiffAddBg
	DiffRemoveBg
)

// Paint wraps s in this colour's ANSI escape pair when colour output
// is enabled. Empty strings pass through untouched and Plain is a
// no-op.
//
// Background colours ([ToolCallBg], [DiffAddBg], [DiffRemoveBg]) also
// re-emit their background escape after every interior `\x1b[0m` in s,
// so a foreground highlighter (e.g. chroma) whose token resets clear
// the background still keeps the bg fill solid across the full line.
func (c Color) Paint(s string) string {
	if !useColor.Load() || s == "" {
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

// restoreBg replaces every interior full-attribute reset in s with
// reset + bg, so a foreground highlighter's per-token reset doesn't
// clear the line's background tint. Called by [Color.Paint] for every
// background colour.
func restoreBg(s, bg string) string {
	if !strings.Contains(s, resetEscape) {
		return s
	}
	return strings.ReplaceAll(s, resetEscape, resetEscape+bg)
}

// fillsLine reports whether this colour wants its segment padded out
// to the full available content width before painting, so its
// background extends to the right edge of the terminal.
func (c Color) fillsLine() bool {
	return c == ToolCallBg || c == DiffAddBg || c == DiffRemoveBg
}

// Line is one logical input line for [WriteBlock]: visible text plus
// the colour applied to each of its visible segments after wrapping.
type Line struct {
	Text  string
	Color Color
}

// markerPrefix returns marker right-padded with spaces to Gutter
// columns. Markers wider than Gutter pass through unchanged — callers
// pass single-rune markers in practice.
func markerPrefix(marker string) string {
	n := utf8.RuneCountInString(marker)
	if n >= Gutter {
		return marker
	}
	return marker + strings.Repeat(" ", Gutter-n)
}

// gutterSpaces returns the continuation prefix used on every
// non-first visible line of a block: Gutter spaces.
func gutterSpaces() string { return strings.Repeat(" ", Gutter) }

// padToWidth right-pads s with spaces so its display width equals
// width. Strings already at or above width pass through untouched.
// Used by [WriteBlock] to fill background-coloured segments out to
// the right edge of the terminal. ANSI escape sequences embedded in s
// do not count toward the visible width; widths are measured by
// [ansi.StringWidth] so wide chars and zero-width joiners are handled
// correctly.
func padToWidth(s string, width int) string {
	n := ansi.StringWidth(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// wrapVisible breaks s into segments whose display width is at most
// max, delegating to [ansi.Hardwrap] so SGR spans are preserved across
// breaks and wide characters are measured correctly. max <= 0 returns
// a single segment unchanged so callers on non-TTYs pass max=0 to
// skip wrapping. Empty input yields a single empty segment so
// [WriteBlock] still emits a marker line for it.
func wrapVisible(s string, max int) []string {
	if max <= 0 {
		return []string{s}
	}
	if s == "" {
		return []string{""}
	}
	wrapped := ansi.Hardwrap(s, max, false)
	return strings.Split(wrapped, "\n")
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
//   - long lines wrap at [TerminalWidth] - [Gutter]; on a non-TTY
//     ([TerminalWidth] returns 0) lines pass through verbatim;
//   - a single trailing newline terminates the last line; trailingBlank
//     adds one more newline after, producing a blank separator.
//
// Callers with no content (lines == nil) get a bare marker line — the
// helper never emits an empty block.
func WriteBlock(w io.Writer, marker string, lines []Line, trailingBlank bool) {
	avail := 0
	if w := TerminalWidth(); w > 0 {
		avail = w - Gutter
	}
	cont := gutterSpaces()
	first := markerPrefix(marker)

	if len(lines) == 0 {
		fmt.Fprintf(w, "%s\n", strings.TrimRight(first, " "))
		if trailingBlank {
			fmt.Fprintln(w)
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
			fmt.Fprintf(w, "%s%s\n", prefix, line.Color.Paint(body))
		}
	}
	if trailingBlank {
		fmt.Fprintln(w)
	}
}

// Decorate writes one logical line — possibly soft-wrapped onto
// several visible lines — prefixed with marker padded to [Gutter]
// columns, painted in dim grey, followed by a blank separator. It is
// the standard treatment for every non-assistant-text status line
// ralph prints.
func Decorate(w io.Writer, marker, text string) {
	WriteBlock(w, marker, []Line{{Text: text, Color: Dim}}, true)
}

// Raw writes a no-colour multi-line block. The first input line gets
// marker padded to [Gutter] columns; later input lines and soft-wrap
// continuations get [Gutter] spaces. Trailing whitespace on each line
// is trimmed; an empty body produces a bare marker line. Unlike
// [Decorate] no trailing blank is emitted — the caller decides whether
// the block terminates a pair.
func Raw(w io.Writer, marker, text string) {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		WriteBlock(w, marker, nil, false)
		return
	}
	parts := strings.Split(text, "\n")
	lines := make([]Line, len(parts))
	for i, l := range parts {
		lines[i] = Line{Text: strings.TrimRight(l, " \t\r")}
	}
	WriteBlock(w, marker, lines, false)
}

// Tool writes a tool-call header block. Same gutter and wrap shape
// as [Raw] / [Lead], but every visible line is painted with the
// subdued [ToolCallBg] fill so the operator can see at a glance
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
func Tool(w io.Writer, marker, text string, trailingBlank bool) {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		WriteBlock(w, marker, nil, trailingBlank)
		return
	}
	parts := strings.Split(text, "\n")
	lines := make([]Line, len(parts))
	for i, l := range parts {
		lines[i] = Line{Text: strings.TrimRight(l, " \t\r"), Color: ToolCallBg}
	}
	WriteBlock(w, marker, lines, trailingBlank)
}

// Lead writes a prose block in the same gutter shape as [Raw] but with
// a trailing blank separator and a light-blue foreground tint. Used
// for assistant text — the colour distinguishes the model's prose
// from tool calls, results, and status lines at a glance. Empty
// bodies emit nothing.
func Lead(w io.Writer, marker, text string) {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		return
	}
	parts := strings.Split(text, "\n")
	lines := make([]Line, len(parts))
	for i, l := range parts {
		lines[i] = Line{Text: strings.TrimRight(l, " \t\r"), Color: LightBlue}
	}
	WriteBlock(w, marker, lines, true)
}

// Header writes the run banner. duration may be any pre-formatted
// string (for example "unlimited" or "4h 0m 0s").
func Header(w io.Writer, version, model, effort, duration string) {
	fmt.Fprintf(w, "ralph v%s\n", version)
	fmt.Fprintf(w, "model=%s effort=%s duration=%s\n\n", model, effort, duration)
}

// FormatBytes renders a byte count as a compact human-readable string
// using base-1024 units up to megabytes.
func FormatBytes(n int) string {
	switch {
	case n == 0:
		return "0b"
	case n < 1024:
		return fmt.Sprintf("%db", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fkb", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fmb", float64(n)/(1024*1024))
	}
}

// FormatMilliseconds renders a duration measured in milliseconds with
// the same scale rules as [FormatElapsed], but at sub-second precision
// for fast tool calls.
func FormatMilliseconds(ms int) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		s := ms / 1000
		m, sec := s/60, s%60
		return fmt.Sprintf("%dm%ds", m, sec)
	}
}

// FormatElapsed renders a whole-seconds duration as "Hh Mm Ss",
// "Mm Ss", or "Ss" depending on magnitude.
func FormatElapsed(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// FormatNumber renders n with a comma between every group of three
// digits, e.g. 1234567 -> "1,234,567".
func FormatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	negative := strings.HasPrefix(s, "-")
	if negative {
		s = s[1:]
	}
	if len(s) <= 3 {
		if negative {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/3)
	if negative {
		b.WriteByte('-')
	}
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}
