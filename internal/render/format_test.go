package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPrimaryArg covers the B-lite header suffix: first-present-wins
// across path/command/pattern, whitespace collapse, rune truncation,
// and the fail-soft empties.
func TestPrimaryArg(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args string
		want string
	}{
		{
			name: "read path",
			args: `{"path":"/tmp/x.go"}`,
			want: "/tmp/x.go",
		},
		{
			name: "bash command",
			args: `{"command":"ls -la","timeout":5000}`,
			want: "ls -la",
		},
		{
			name: "grep pattern",
			args: `{"pattern":"TODO.*fix"}`,
			want: "TODO.*fix",
		},
		{
			name: "path wins over command (precedence order)",
			args: `{"command":"cmd","path":"/p"}`,
			want: "/p",
		},
		{
			name: "edit path with edits array",
			args: `{"path":"f","edits":[{"oldText":"a","newText":"b"}]}`,
			want: "f",
		},
		{
			name: "multiline command collapses to one line",
			args: `{"command":"echo a\necho b"}`,
			want: "echo a echo b",
		},
		{
			name: "long value is truncated to 60 runes with ellipsis",
			args: `{"command":"` + strings.Repeat("x", 100) + `"}`,
			want: strings.Repeat("x", 59) + "…",
		},
		{
			name: "no primary key yields empty",
			args: `{"timeout":5000}`,
			want: "",
		},
		{
			name: "empty object yields empty",
			args: `{}`,
			want: "",
		},
		{
			name: "absent args yields empty",
			args: ``,
			want: "",
		},
		{
			name: "garbage args is fail-soft",
			args: `not json`,
			want: "",
		},
		{
			name: "non-string primary arg falls back to raw json",
			args: `{"path":123}`,
			want: "123",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := primaryArg(json.RawMessage(tc.args))
			if got != tc.want {
				t.Errorf("primaryArg(%s) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestResultContentText covers the pi result envelope: a single text
// element, multiple concatenated text elements, a non-text element
// skipped, and the fail-soft empties.
func TestResultContentText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single text element",
			in:   `{"content":[{"type":"text","text":"alpha\nbeta\n"}]}`,
			want: "alpha\nbeta\n",
		},
		{
			name: "multiple text elements concatenate",
			in:   `{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`,
			want: "ab",
		},
		{
			name: "non-text element is skipped",
			in:   `{"content":[{"type":"image","text":"ignored"},{"type":"text","text":"kept"}]}`,
			want: "kept",
		},
		{
			name: "details are ignored",
			in:   `{"content":[{"type":"text","text":"ok"}],"details":{"diff":"x"}}`,
			want: "ok",
		},
		{
			name: "empty content array",
			in:   `{"content":[]}`,
			want: "",
		},
		{
			name: "absent result",
			in:   ``,
			want: "",
		},
		{
			name: "unparseable result is fail-soft",
			in:   `not json`,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resultContentText(json.RawMessage(tc.in))
			if got != tc.want {
				t.Errorf("resultContentText(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCollapseWhitespace pins the one-line normalisation: every run of
// whitespace becomes a single space and leading/trailing space is
// trimmed.
func TestCollapseWhitespace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"  a   b\tc\n\nd  ", "a b c d"},
		{"plain", "plain"},
		{"   ", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := collapseWhitespace(tc.in); got != tc.want {
			t.Errorf("collapseWhitespace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTruncateRunes confirms truncation is rune-aware (never splits a
// multi-byte character) and only triggers past the limit.
func TestTruncateRunes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"short", 60, "short"},
		{"exactly5", 8, "exactly5"},
		{"abcdef", 4, "abc…"},
		{"日本語テスト", 4, "日本語…"},
	}
	for _, tc := range cases {
		if got := truncateRunes(tc.in, tc.limit); got != tc.want {
			t.Errorf("truncateRunes(%q,%d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
}
