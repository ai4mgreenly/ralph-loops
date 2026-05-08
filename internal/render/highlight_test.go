package render

import (
	"strings"
	"testing"
)

// TestBalanceSGRPerLine_PassesThroughBalanced asserts that lines whose
// SGR escapes already pair open/close on the same line are emitted
// unchanged: no synthetic reset is appended and no carry-in is
// prepended on the next line.
func TestBalanceSGRPerLine_PassesThroughBalanced(t *testing.T) {
	t.Parallel()
	in := []string{
		"\x1b[31mred\x1b[0m and plain",
		"plain again",
	}
	got := balanceSGRPerLine(in)
	if len(got) != len(in) {
		t.Fatalf("len(got)=%d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("line %d altered: got %q, want %q", i, got[i], in[i])
		}
	}
}

// TestBalanceSGRPerLine_TableDriven covers the menagerie of edge cases
// a chroma-driven highlighter can produce: open spans crossing a line,
// embedded resets, multiple stacked SGR codes, plain text, the empty
// case, and lines that are nothing but a single SGR escape.
func TestBalanceSGRPerLine_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "empty input",
			in:   nil,
			want: nil,
		},
		{
			name: "plain text passes through unchanged",
			in:   []string{"foo", "bar"},
			want: []string{"foo", "bar"},
		},
		{
			name: "unbalanced open gets synthesized reset and re-open on next line",
			in: []string{
				"\x1b[31mhello",
				"world\x1b[0m",
			},
			want: []string{
				"\x1b[31mhello\x1b[0m",
				"\x1b[31mworld\x1b[0m",
			},
		},
		{
			name: "embedded reset terminates carry",
			in: []string{
				"\x1b[31mred\x1b[0m plain",
				"next",
			},
			want: []string{
				"\x1b[31mred\x1b[0m plain",
				"next",
			},
		},
		{
			name: "multiple SGR codes stack and carry",
			in: []string{
				"\x1b[31m\x1b[1mred-bold",
				"still\x1b[0m done",
			},
			want: []string{
				"\x1b[31m\x1b[1mred-bold\x1b[0m",
				"\x1b[31m\x1b[1mstill\x1b[0m done",
			},
		},
		{
			name: "line containing only an SGR escape",
			in: []string{
				"\x1b[31m",
				"after",
			},
			want: []string{
				"\x1b[31m\x1b[0m",
				"\x1b[31mafter\x1b[0m",
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := balanceSGRPerLine(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d, want %d\ngot:  %q\nwant: %q", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("line %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestScanSGR_ReturnsEscapesInOrder pins the helper that
// balanceSGRPerLine uses to track open/close state.
func TestScanSGR_ReturnsEscapesInOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "no escape",
			in:   "plain text",
			want: nil,
		},
		{
			name: "single escape",
			in:   "\x1b[31mred",
			want: []string{"\x1b[31m"},
		},
		{
			name: "open and reset",
			in:   "\x1b[31mred\x1b[0m",
			want: []string{"\x1b[31m", "\x1b[0m"},
		},
		{
			name: "stacked SGRs",
			in:   "\x1b[31m\x1b[1mhi\x1b[22m\x1b[39m",
			want: []string{"\x1b[31m", "\x1b[1m", "\x1b[22m", "\x1b[39m"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scanSGR(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d, want %d\ngot:  %q\nwant: %q", len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("escape %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestIsReset accepts the two canonical reset forms and rejects
// everything else.
func TestIsReset(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"\x1b[0m", true},
		{"\x1b[m", true},
		{"\x1b[31m", false},
		{"\x1b[1;31m", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isReset(tc.in); got != tc.want {
			t.Errorf("isReset(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestHighlightLines_FallsBackWhenColorDisabled confirms the public
// entry point hands back the splitLinesNoTrailing result whenever
// color is disabled, regardless of file path.
func TestHighlightLines_FallsBackWhenColorDisabled(t *testing.T) {
	t.Parallel()
	got := highlightLines("foo.go", "package x\nfunc Y() {}", false)
	want := []string{"package x", "func Y() {}"}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestHighlightLines_NoLexerFallsBack confirms that an unknown file
// extension produces plain split lines, no chroma escapes.
func TestHighlightLines_NoLexerFallsBack(t *testing.T) {
	t.Parallel()
	got := highlightLines("file.unknownext", "raw\nlines", true)
	if len(got) != 2 || got[0] != "raw" || got[1] != "lines" {
		t.Errorf("expected plain fallback, got %q", got)
	}
	for _, line := range got {
		if strings.ContainsRune(line, 0x1b) {
			t.Errorf("plain fallback should have no SGR escapes, got %q", line)
		}
	}
}
