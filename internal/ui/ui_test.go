package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// newTestTheme constructs a Theme with the given colour and width
// settings. Tests pass useColor=false when asserting raw strings
// without ANSI escapes, useColor=true when asserting on the painted
// output.
func newTestTheme(t *testing.T, useColor bool, width int) *Theme {
	t.Helper()
	return NewThemeWith(useColor, width)
}

func TestWriteBlock_FirstLineMarkerAndContinuation(t *testing.T) {
	th := newTestTheme(t, false, 0)
	var buf bytes.Buffer
	th.WriteBlock(&buf, "→", []Line{
		{Text: "first"},
		{Text: "second"},
	}, false)
	want := "→  first\n   second\n"
	if got := buf.String(); got != want {
		t.Errorf("multi-line block:\ngot  %q\nwant %q", got, want)
	}
}

func TestWriteBlock_SoftWrapPreservesGutter(t *testing.T) {
	th := newTestTheme(t, false, 13) // 13 - Gutter(3) = 10 visible per line
	var buf bytes.Buffer
	th.WriteBlock(&buf, "→", []Line{{Text: "abcdefghijKLMNO"}}, false)
	// First segment uses marker prefix; the wrap continuation gets
	// Gutter spaces only — no marker, no diff sigil re-added.
	want := "→  abcdefghij\n   KLMNO\n"
	if got := buf.String(); got != want {
		t.Errorf("soft-wrap shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestWriteBlock_SoftWrapBetweenLogicalLines(t *testing.T) {
	th := newTestTheme(t, false, 13) // 10 visible runes per line
	var buf bytes.Buffer
	th.WriteBlock(&buf, "→", []Line{
		{Text: "+ short"},
		{Text: "+ this one is long enough to wrap"},
		{Text: "+ tail"},
	}, false)
	// Wrap continuations on the long inner line must not bleed into
	// the next logical line's marker space — every continuation is a
	// bare 3-space gutter.
	want := strings.Join([]string{
		"→  + short",
		"   + this one",
		"   is long en",
		"   ough to wr",
		"   ap",
		"   + tail",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("inner wrap shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestWriteBlock_NonTTYNoWrap(t *testing.T) {
	th := newTestTheme(t, false, 0)
	var buf bytes.Buffer
	long := strings.Repeat("x", 200)
	th.WriteBlock(&buf, "→", []Line{{Text: long}}, false)
	want := "→  " + long + "\n"
	if got := buf.String(); got != want {
		t.Errorf("non-TTY long line should pass through; got %q", got)
	}
}

func TestWriteBlock_TrailingBlank(t *testing.T) {
	th := newTestTheme(t, false, 0)
	var buf bytes.Buffer
	th.WriteBlock(&buf, "→", []Line{{Text: "x"}}, true)
	if got := buf.String(); got != "→  x\n\n" {
		t.Errorf("trailing blank: got %q", got)
	}
}

func TestWriteBlock_EmptyLinesProducesBareMarker(t *testing.T) {
	th := newTestTheme(t, false, 0)
	var buf bytes.Buffer
	th.WriteBlock(&buf, "→", nil, true)
	if got := buf.String(); got != "→\n\n" {
		t.Errorf("empty block: got %q", got)
	}
}

func TestWriteBlock_PerLineColorAfterWrap(t *testing.T) {
	th := newTestTheme(t, true, 8) // 8 - 3 = 5 visible per line

	var buf bytes.Buffer
	th.WriteBlock(&buf, "→", []Line{{Text: "abcdefghij", Color: DimRed}}, false)
	got := buf.String()
	// Two segments of 5 visible runes each, each individually wrapped
	// in dim-red ANSI. Gutter prefixes are *outside* the colour.
	wantSegments := []string{
		"→  " + dimRedEscape + "abcde" + resetEscape + "\n",
		"   " + dimRedEscape + "fghij" + resetEscape + "\n",
	}
	want := wantSegments[0] + wantSegments[1]
	if got != want {
		t.Errorf("colour-after-wrap:\ngot  %q\nwant %q", got, want)
	}
}

func TestWrapVisible_PreservesVisibleContentAcrossANSI(t *testing.T) {
	// Wrap a string with embedded SGR escapes; the concatenated visible
	// content (escapes stripped) must equal the original visible content,
	// and the total display width per segment must not exceed the limit.
	input := "abc\x1b[31mdefghi\x1b[0mjklmno"
	const limit = 5
	got := wrapVisible(input, limit)
	if len(got) < 2 {
		t.Fatalf("wrap produced %d segments (%q); want >=2", len(got), got)
	}
	stripVisible := func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s); {
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				for j < len(s) {
					c := s[j]
					j++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
				i = j
				continue
			}
			b.WriteByte(s[i])
			i++
		}
		return b.String()
	}
	var rejoined strings.Builder
	for _, seg := range got {
		rejoined.WriteString(stripVisible(seg))
	}
	if want := "abcdefghijklmno"; rejoined.String() != want {
		t.Errorf("rejoined visible content = %q, want %q", rejoined.String(), want)
	}
}

func TestWrapVisible_EmptyAndNoWrap(t *testing.T) {
	if got := wrapVisible("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("empty input: got %q, want [\"\"]", got)
	}
	if got := wrapVisible("anything", 0); len(got) != 1 || got[0] != "anything" {
		t.Errorf("max=0 should pass through: got %q", got)
	}
}

func TestPaint_BgRestoresAfterInteriorReset(t *testing.T) {
	th := newTestTheme(t, true, 0)
	// A foreground span inside a bg paint: the inner reset must be
	// followed by a re-emission of the bg so the line tint stays solid.
	got := th.Paint(DiffAddBg, "\x1b[31mabc\x1b[0mdef")
	want := diffAddBgEscape + "\x1b[31mabc" + resetEscape + diffAddBgEscape + "def" + resetEscape
	if got != want {
		t.Errorf("bg restoration:\ngot  %q\nwant %q", got, want)
	}
}

func TestPaint_DiffRemoveBg_NoInteriorReset(t *testing.T) {
	th := newTestTheme(t, true, 0)
	got := th.Paint(DiffRemoveBg, "plain")
	want := diffRemoveBgEscape + "plain" + resetEscape
	if got != want {
		t.Errorf("plain bg paint:\ngot  %q\nwant %q", got, want)
	}
}

func TestTool_BackgroundFillsToEdge(t *testing.T) {
	th := newTestTheme(t, true, 13) // 10 visible runes per line

	var buf bytes.Buffer
	th.Tool(&buf, "←", "ls", false)
	got := buf.String()
	// Short content "ls" must be padded out to 10 runes inside the
	// background escape so the fill reaches the right edge. The gutter
	// (`←  `) sits outside the colour entirely.
	want := "←  " + toolCallBgEscape + "ls" + strings.Repeat(" ", 8) + resetEscape + "\n"
	if got != want {
		t.Errorf("Tool fill-to-edge:\ngot  %q\nwant %q", got, want)
	}
}

func TestTool_BackgroundOnEveryWrappedLine(t *testing.T) {
	th := newTestTheme(t, true, 13) // 10 visible runes per line

	var buf bytes.Buffer
	th.Tool(&buf, "←", "abcdefghijKLM", false) // 13 runes -> 10 + 3
	got := buf.String()
	// Both segments — the wrap and its continuation — get the bg
	// escape, padded to width. The continuation gutter (3 spaces)
	// stays outside the colour.
	want := "←  " + toolCallBgEscape + "abcdefghij" + resetEscape + "\n" +
		"   " + toolCallBgEscape + "KLM" + strings.Repeat(" ", 7) + resetEscape + "\n"
	if got != want {
		t.Errorf("Tool wrap+fill:\ngot  %q\nwant %q", got, want)
	}
}

func TestTool_NonTTYDropsFillAndColor(t *testing.T) {
	th := newTestTheme(t, false, 0) // non-TTY: no wrap, no fill
	var buf bytes.Buffer
	th.Tool(&buf, "←", "ls", false)
	if got := buf.String(); got != "←  ls\n" {
		t.Errorf("Tool non-TTY: got %q", got)
	}
}

func TestDecorate_UsesGutterAndDim(t *testing.T) {
	th := newTestTheme(t, false, 0)
	var buf bytes.Buffer
	th.Decorate(&buf, "↑", "assistant (empty)")
	want := "↑  assistant (empty)\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Decorate shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestRaw_NoTrailingBlank(t *testing.T) {
	th := newTestTheme(t, false, 0)
	var buf bytes.Buffer
	th.Raw(&buf, "←", "ls\nfoo")
	want := "←  ls\n   foo\n"
	if got := buf.String(); got != want {
		t.Errorf("Raw shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestLead_TrailingBlank(t *testing.T) {
	th := newTestTheme(t, false, 0)
	var buf bytes.Buffer
	th.Lead(&buf, "*", "hello\nworld")
	want := "*  hello\n   world\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Lead shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestLead_PaintsLightBlue(t *testing.T) {
	th := newTestTheme(t, true, 0)

	var buf bytes.Buffer
	th.Lead(&buf, "*", "hello\nworld")
	want := "*  " + lightBlueEscape + "hello" + resetEscape + "\n" +
		"   " + lightBlueEscape + "world" + resetEscape + "\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Lead colour:\ngot  %q\nwant %q", got, want)
	}
}

func TestFormatMilliseconds_AllRanges(t *testing.T) {
	tests := []struct {
		ms   int
		want string
	}{
		{0, "0ms"},
		{500, "500ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{45_500, "45.5s"},
		{60_000, "1m0s"},
		{125_000, "2m5s"},
	}
	for _, tc := range tests {
		if got := FormatMilliseconds(tc.ms); got != tc.want {
			t.Errorf("FormatMilliseconds(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestFormatBytes_AllRanges(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0B"},
		{1, "1B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.0MB"},
		{2_500_000, "2.4MB"},
	}
	for _, tc := range tests {
		if got := FormatBytes(tc.n); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestFormatElapsed_AllRanges(t *testing.T) {
	tests := []struct {
		seconds int
		want    string
	}{
		{-5, "0s"},
		{0, "0s"},
		{45, "45s"},
		{60, "1m 0s"},
		{125, "2m 5s"},
		{3600, "1h 0m 0s"},
		{3661, "1h 1m 1s"},
	}
	for _, tc := range tests {
		if got := FormatElapsed(tc.seconds); got != tc.want {
			t.Errorf("FormatElapsed(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestNewTheme_NonTTYConstructsSaneTheme(t *testing.T) {
	t.Parallel()
	// Open a regular file as a non-TTY *os.File. detectColor should
	// return false (not a char device), detectWidth should return 0.
	dir := t.TempDir()
	path := dir + "/out"
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()

	th := NewTheme(f)
	if th == nil {
		t.Fatal("NewTheme returned nil")
	}
	if th.UseColor() {
		t.Error("UseColor should be false on non-TTY regular file")
	}
	if th.Width() != 0 {
		t.Errorf("Width on non-TTY = %d, want 0", th.Width())
	}
}

func TestTheme_NilSafe(t *testing.T) {
	t.Parallel()
	var th *Theme
	if th.UseColor() {
		t.Error("nil theme UseColor should be false")
	}
	if th.Width() != 0 {
		t.Error("nil theme Width should be 0")
	}
	// SetWidth on nil should not panic.
	th.SetWidth(99)
	// Paint on nil should pass through.
	if got := th.Paint(Dim, "hi"); got != "hi" {
		t.Errorf("nil Paint = %q, want %q", got, "hi")
	}
}

func TestTheme_SetWidthAndRule(t *testing.T) {
	t.Parallel()
	th := NewThemeWith(false, 0)
	if got := th.Rule(); got != strings.Repeat(RuleChar, RuleFallbackWidth) {
		t.Errorf("Rule fallback length = %d, want %d", len(got), RuleFallbackWidth)
	}
	th.SetWidth(15)
	if th.Width() != 15 {
		t.Errorf("Width after SetWidth = %d, want 15", th.Width())
	}
	if got := th.Rule(); got != strings.Repeat(RuleChar, 15) {
		t.Errorf("Rule at width 15 = %q", got)
	}
}

func TestBuildRule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		width int
		want  string
	}{
		{"positive", 5, strings.Repeat(RuleChar, 5)},
		{"zero falls back", 0, strings.Repeat(RuleChar, RuleFallbackWidth)},
		{"negative falls back", -3, strings.Repeat(RuleChar, RuleFallbackWidth)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := BuildRule(tc.width); got != tc.want {
				t.Errorf("BuildRule(%d) length=%d, want %d", tc.width, len(got), len(tc.want))
			}
		})
	}
}

func TestTheme_PaintAllColors(t *testing.T) {
	t.Parallel()
	th := NewThemeWith(true, 0)
	tests := []struct {
		name  string
		color Color
		in    string
		want  string
	}{
		{"dim", Dim, "x", dimEscape + "x" + resetEscape},
		{"dimRed", DimRed, "x", dimRedEscape + "x" + resetEscape},
		{"dimGreen", DimGreen, "x", dimGreenEscape + "x" + resetEscape},
		{"lightBlue", LightBlue, "x", lightBlueEscape + "x" + resetEscape},
		{"orange", Orange, "x", orangeEscape + "x" + resetEscape},
		{"bright", Bright, "x", brightEscape + "x" + resetEscape},
		{"toolCallBg", ToolCallBg, "x", toolCallBgEscape + "x" + resetEscape},
		{"plain-passthrough", Plain, "x", "x"},
		{"empty-passthrough", Dim, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := th.Paint(tc.color, tc.in); got != tc.want {
				t.Errorf("Paint(%v, %q) = %q, want %q", tc.color, tc.in, got, tc.want)
			}
		})
	}
}

func TestTheme_PaintNoColorPassthrough(t *testing.T) {
	t.Parallel()
	th := NewThemeWith(false, 0)
	if got := th.Paint(DimRed, "x"); got != "x" {
		t.Errorf("Paint with color disabled = %q, want %q", got, "x")
	}
}

func TestTheme_RawEmptyAndContent(t *testing.T) {
	t.Parallel()
	th := NewThemeWith(false, 0)

	// Empty body: bare marker, no trailing blank.
	var buf bytes.Buffer
	th.Raw(&buf, "←", "   \n\t")
	if got := buf.String(); got != "←\n" {
		t.Errorf("Raw empty: got %q, want %q", got, "←\n")
	}

	// Content: standard gutter shape, no trailing blank.
	buf.Reset()
	th.Raw(&buf, "←", "ls -la\nfoo")
	want := "←  ls -la\n   foo\n"
	if got := buf.String(); got != want {
		t.Errorf("Raw content: got %q, want %q", got, want)
	}
}

func TestTheme_ToolEmpty(t *testing.T) {
	t.Parallel()
	th := NewThemeWith(false, 0)
	var buf bytes.Buffer
	th.Tool(&buf, "←", "  \t\n", true)
	if got := buf.String(); got != "←\n\n" {
		t.Errorf("Tool empty + trailingBlank: got %q", got)
	}
}

func TestTheme_LeadEmpty(t *testing.T) {
	t.Parallel()
	th := NewThemeWith(false, 0)
	var buf bytes.Buffer
	th.Lead(&buf, "*", "  \n")
	if got := buf.String(); got != "" {
		t.Errorf("Lead empty should emit nothing, got %q", got)
	}
}

func TestExpandTabs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no tabs passthrough", "abc", "abc"},
		{"single tab", "a\tb", "a    b"},
		{"multiple tabs", "\t\t", strings.Repeat(" ", 8)},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := expandTabs(tc.in); got != tc.want {
				t.Errorf("expandTabs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under-max passthrough", "abc", 5, "abc"},
		{"at-max passthrough", "abcde", 5, "abcde"},
		{"over-max truncates", "abcdef", 5, "abcd…"},
		{"max=1 returns ellipsis", "abcdef", 1, "…"},
		{"max=0 passthrough", "abc", 0, "abc"},
		{"negative max passthrough", "abc", -1, "abc"},
		{"unicode handled by runes", "αβγδε", 3, "αβ…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Truncate(tc.in, tc.max); got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestColor_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		c    Color
		want string
	}{
		{Plain, "plain"},
		{Dim, "dim"},
		{DimRed, "dimRed"},
		{DimGreen, "dimGreen"},
		{LightBlue, "lightBlue"},
		{Orange, "orange"},
		{Bright, "bright"},
		{ToolCallBg, "toolCallBg"},
		{DiffAddBg, "diffAddBg"},
		{DiffRemoveBg, "diffRemoveBg"},
		{Color(99), "Color(99)"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.c.String(); got != tc.want {
				t.Errorf("Color(%d).String() = %q, want %q", int(tc.c), got, tc.want)
			}
		})
	}
}

func TestHeader(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	Header(&buf, "1.2.3", "opus", "medium", "unlimited")
	want := "ralph v1.2.3\nmodel=opus effort=medium duration=unlimited\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Header:\ngot  %q\nwant %q", got, want)
	}
}

func TestFormatNumber_AllRanges(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{-1234567, "-1,234,567"},
	}
	for _, tc := range tests {
		if got := FormatNumber(tc.n); got != tc.want {
			t.Errorf("FormatNumber(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
