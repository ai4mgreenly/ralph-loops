// Package ui contains the human-facing output helpers used by the
// iteration driver: decorated status lines, byte and time formatters,
// and number formatting with thousands separators.
//
// Output is parameterised on a [Theme] value the caller constructs
// (typically once, at startup) and threads through whichever code
// renders to a terminal. ANSI colour escapes are emitted only when
// the Theme's stdout is a terminal and the NO_COLOR environment
// variable is unset, in line with https://no-color.org. The
// pure-formatter helpers ([FormatBytes], [FormatNumber],
// [FormatElapsed], [FormatMilliseconds], [Truncate]) read no shared
// state and stay free functions.
//
// The package has no init-time side effects: importing it does not
// inspect stdout or the environment. All such detection happens
// inside [NewTheme].
package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// tabWidth is the number of spaces used to expand a `\t` character
// before any width calculation. Tabs cannot be measured reliably —
// terminals advance them to a column-modulo tab stop, while
// [ansi.StringWidth] reports them as zero-width — so block content
// is normalised to spaces at the entry to [Theme.WriteBlock] and
// every downstream wrap, pad, and paint operation sees a tab-free
// string. Four matches the convention most source files use; the
// value is intentionally not configurable.
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

// Color paints a visible segment after wrapping. Used by
// [Theme.WriteBlock] so ANSI escape sequences are not counted toward
// the wrap budget.
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

// String returns a human-readable name for the color, suitable for
// debug and log output.
func (c Color) String() string {
	switch c {
	case Plain:
		return "plain"
	case Dim:
		return "dim"
	case DimRed:
		return "dimRed"
	case DimGreen:
		return "dimGreen"
	case LightBlue:
		return "lightBlue"
	case Orange:
		return "orange"
	case Bright:
		return "bright"
	case ToolCallBg:
		return "toolCallBg"
	case DiffAddBg:
		return "diffAddBg"
	case DiffRemoveBg:
		return "diffRemoveBg"
	default:
		return fmt.Sprintf("Color(%d)", int(c))
	}
}

// restoreBg replaces every interior full-attribute reset in s with
// reset + bg, so a foreground highlighter's per-token reset doesn't
// clear the line's background tint. Called by [Theme.Paint] for
// every background colour.
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

// Line is one logical input line for [Theme.WriteBlock]: visible
// text plus the colour applied to each of its visible segments after
// wrapping.
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

// wrapVisible breaks s into segments whose display width is at most
// max, delegating to [ansi.Hardwrap] so SGR spans are preserved across
// breaks and wide characters are measured correctly. max <= 0 returns
// a single segment unchanged so callers on non-TTYs pass max=0 to
// skip wrapping. Empty input yields a single empty segment so
// [Theme.WriteBlock] still emits a marker line for it.
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

// Header writes the run banner. duration may be any pre-formatted
// string (for example "unlimited" or "4h 0m 0s"). engine is the
// command name of the agent CLI being driven (typically "pi").
//
// version is printed verbatim — it already carries any prefix the
// build wants ("v0.1.0", "v0.1.0-3-gabc1234", "dev"). Adding our own
// "v" here would double-prefix tag-derived strings into "vv0.1.0".
func Header(w io.Writer, version, engine, model, effort, duration string) {
	fmt.Fprintf(w, "ralph %s\n", version)
	fmt.Fprintf(w, "engine=%s model=%s effort=%s duration=%s\n", engine, model, effort, duration)
}

// FormatBytes renders a byte count as a compact human-readable string
// using base-1024 units up to megabytes.
func FormatBytes(n int) string {
	switch {
	case n == 0:
		return "0B"
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/(1024*1024*1024))
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
