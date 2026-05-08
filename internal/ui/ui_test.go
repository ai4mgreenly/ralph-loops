package ui

import (
	"bytes"
	"strings"
	"testing"
)

// withTerminalWidth temporarily overrides the detected terminal width
// for the duration of the test, restoring the previous value on
// cleanup. SetColor(false) is implied so the assertions can compare
// raw strings without ANSI escapes.
func withTerminalWidth(t *testing.T, width int) {
	t.Helper()
	prevW := TerminalWidth()
	prevC := useColor.Load()
	SetTerminalWidth(width)
	SetColor(false)
	t.Cleanup(func() {
		SetTerminalWidth(prevW)
		SetColor(prevC)
	})
}

func TestWriteBlock_FirstLineMarkerAndContinuation(t *testing.T) {
	withTerminalWidth(t, 0)
	var buf bytes.Buffer
	WriteBlock(&buf, "→", []Line{
		{Text: "first"},
		{Text: "second"},
	}, false)
	want := "→  first\n   second\n"
	if got := buf.String(); got != want {
		t.Errorf("multi-line block:\ngot  %q\nwant %q", got, want)
	}
}

func TestWriteBlock_SoftWrapPreservesGutter(t *testing.T) {
	withTerminalWidth(t, 13) // 13 - Gutter(3) = 10 visible per line
	var buf bytes.Buffer
	WriteBlock(&buf, "→", []Line{{Text: "abcdefghijKLMNO"}}, false)
	// First segment uses marker prefix; the wrap continuation gets
	// Gutter spaces only — no marker, no diff sigil re-added.
	want := "→  abcdefghij\n   KLMNO\n"
	if got := buf.String(); got != want {
		t.Errorf("soft-wrap shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestWriteBlock_SoftWrapBetweenLogicalLines(t *testing.T) {
	withTerminalWidth(t, 13) // 10 visible runes per line
	var buf bytes.Buffer
	WriteBlock(&buf, "→", []Line{
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
	withTerminalWidth(t, 0)
	var buf bytes.Buffer
	long := strings.Repeat("x", 200)
	WriteBlock(&buf, "→", []Line{{Text: long}}, false)
	want := "→  " + long + "\n"
	if got := buf.String(); got != want {
		t.Errorf("non-TTY long line should pass through; got %q", got)
	}
}

func TestWriteBlock_TrailingBlank(t *testing.T) {
	withTerminalWidth(t, 0)
	var buf bytes.Buffer
	WriteBlock(&buf, "→", []Line{{Text: "x"}}, true)
	if got := buf.String(); got != "→  x\n\n" {
		t.Errorf("trailing blank: got %q", got)
	}
}

func TestWriteBlock_EmptyLinesProducesBareMarker(t *testing.T) {
	withTerminalWidth(t, 0)
	var buf bytes.Buffer
	WriteBlock(&buf, "→", nil, true)
	if got := buf.String(); got != "→\n\n" {
		t.Errorf("empty block: got %q", got)
	}
}

func TestWriteBlock_PerLineColorAfterWrap(t *testing.T) {
	withTerminalWidth(t, 8) // 8 - 3 = 5 visible per line
	prevC := useColor.Load()
	SetColor(true)
	t.Cleanup(func() { SetColor(prevC) })

	var buf bytes.Buffer
	WriteBlock(&buf, "→", []Line{{Text: "abcdefghij", Color: DimRed}}, false)
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
	prev := useColor.Load()
	SetColor(true)
	t.Cleanup(func() { SetColor(prev) })

	// A foreground span inside a bg paint: the inner reset must be
	// followed by a re-emission of the bg so the line tint stays solid.
	got := DiffAddBg.Paint("\x1b[31mabc\x1b[0mdef")
	want := diffAddBgEscape + "\x1b[31mabc" + resetEscape + diffAddBgEscape + "def" + resetEscape
	if got != want {
		t.Errorf("bg restoration:\ngot  %q\nwant %q", got, want)
	}
}

func TestPaint_DiffRemoveBg_NoInteriorReset(t *testing.T) {
	prev := useColor.Load()
	SetColor(true)
	t.Cleanup(func() { SetColor(prev) })

	got := DiffRemoveBg.Paint("plain")
	want := diffRemoveBgEscape + "plain" + resetEscape
	if got != want {
		t.Errorf("plain bg paint:\ngot  %q\nwant %q", got, want)
	}
}

func TestTool_BackgroundFillsToEdge(t *testing.T) {
	withTerminalWidth(t, 13) // 10 visible runes per line
	prevC := useColor.Load()
	SetColor(true)
	t.Cleanup(func() { SetColor(prevC) })

	var buf bytes.Buffer
	Tool(&buf, "←", "ls", false)
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
	withTerminalWidth(t, 13) // 10 visible runes per line
	prevC := useColor.Load()
	SetColor(true)
	t.Cleanup(func() { SetColor(prevC) })

	var buf bytes.Buffer
	Tool(&buf, "←", "abcdefghijKLM", false) // 13 runes -> 10 + 3
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
	withTerminalWidth(t, 0) // non-TTY: no wrap, no fill
	var buf bytes.Buffer
	Tool(&buf, "←", "ls", false)
	if got := buf.String(); got != "←  ls\n" {
		t.Errorf("Tool non-TTY: got %q", got)
	}
}

func TestDecorate_UsesGutterAndDim(t *testing.T) {
	withTerminalWidth(t, 0)
	var buf bytes.Buffer
	Decorate(&buf, "↑", "assistant (empty)")
	want := "↑  assistant (empty)\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Decorate shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestRaw_NoTrailingBlank(t *testing.T) {
	withTerminalWidth(t, 0)
	var buf bytes.Buffer
	Raw(&buf, "←", "ls\nfoo")
	want := "←  ls\n   foo\n"
	if got := buf.String(); got != want {
		t.Errorf("Raw shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestLead_TrailingBlank(t *testing.T) {
	withTerminalWidth(t, 0)
	var buf bytes.Buffer
	Lead(&buf, "*", "hello\nworld")
	want := "*  hello\n   world\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Lead shape:\ngot  %q\nwant %q", got, want)
	}
}

func TestLead_PaintsLightBlue(t *testing.T) {
	withTerminalWidth(t, 0)
	prevC := useColor.Load()
	SetColor(true)
	t.Cleanup(func() { SetColor(prevC) })

	var buf bytes.Buffer
	Lead(&buf, "*", "hello\nworld")
	want := "*  " + lightBlueEscape + "hello" + resetEscape + "\n" +
		"   " + lightBlueEscape + "world" + resetEscape + "\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Lead colour:\ngot  %q\nwant %q", got, want)
	}
}

func TestFormatMilliseconds(t *testing.T) {
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

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0b"},
		{1, "1b"},
		{1023, "1023b"},
		{1024, "1.0kb"},
		{1536, "1.5kb"},
		{1024 * 1024, "1.0mb"},
		{2_500_000, "2.4mb"},
	}
	for _, tc := range tests {
		if got := FormatBytes(tc.n); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestFormatElapsed(t *testing.T) {
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

func TestFormatNumber(t *testing.T) {
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
