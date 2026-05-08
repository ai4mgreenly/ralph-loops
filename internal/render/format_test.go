package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

func TestFormatToolCallParam(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{
			name:  "bash with command",
			tool:  "Bash",
			input: `{"command":"ls -la","timeout":5000}`,
			want:  `command="ls -la"`,
		},
		{
			name:  "read with file_path",
			tool:  "Read",
			input: `{"file_path":"/tmp/x.go"}`,
			want:  `file_path="/tmp/x.go"`,
		},
		{
			name:  "grep with pattern",
			tool:  "Grep",
			input: `{"pattern":"TODO.*fix","path":"/src"}`,
			want:  `pattern="TODO.*fix"`,
		},
		{
			name:  "unknown tool falls back to first key",
			tool:  "WeirdTool",
			input: `{"only":"value"}`,
			want:  `only="value"`,
		},
		{
			name:  "long command is truncated",
			tool:  "Bash",
			input: `{"command":"` + strings.Repeat("x", 100) + `"}`,
			want:  `command="` + strings.Repeat("x", 59) + `…"`,
		},
		{
			name:  "multiline command collapses to one line",
			tool:  "Bash",
			input: `{"command":"echo a\necho b"}`,
			want:  `command="echo a echo b"`,
		},
		{
			name:  "missing primary key shows ellipsis",
			tool:  "Bash",
			input: `{"timeout":5000}`,
			want:  `timeout=…`,
		},
		{
			name:  "empty input returns empty",
			tool:  "Bash",
			input: `{}`,
			want:  ``,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatToolCallParam(tc.tool, json.RawMessage(tc.input))
			if got != tc.want {
				t.Errorf("formatToolCallParam(%q, %s) = %q, want %q", tc.tool, tc.input, got, tc.want)
			}
		})
	}
}

// TestFormatToolCallParam_DeterministicFallback exercises the
// "missing primary key" and "unknown tool" branches that fall back to
// "first key in input". Go's map iteration order is randomized, so a
// naïve `for k := range m { return k }` made the formatter's output
// non-deterministic across runs. Run the formatter many times against
// the same multi-key input and assert every call returns the same
// string.
func TestFormatToolCallParam_DeterministicFallback(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
	}{
		{
			name:  "missing primary key chooses sorted-first",
			tool:  "Bash",
			input: `{"timeout":5000,"shell":"zsh","quiet":true,"abort":false}`,
		},
		{
			name:  "unknown tool falls back to sorted-first key",
			tool:  "WeirdTool",
			input: `{"zeta":1,"alpha":2,"middle":3,"yankee":4}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := formatToolCallParam(tc.tool, json.RawMessage(tc.input))
			if first == "" {
				t.Fatalf("expected non-empty result, got empty for input %s", tc.input)
			}
			for i := 0; i < 200; i++ {
				got := formatToolCallParam(tc.tool, json.RawMessage(tc.input))
				if got != first {
					t.Fatalf("nondeterministic output: iteration %d returned %q, first call returned %q (input %s)", i, got, first, tc.input)
				}
			}
		})
	}
}

func TestFormatToolResult(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		block      stream.Block
		structured string
		want       string
	}{
		{
			name:       "bash with stdout and stderr",
			tool:       "Bash",
			structured: `{"stdout":"hello\n","stderr":"warn\n"}`,
			want:       "stdout=6b stderr=5b",
		},
		{
			name:       "bash interrupted",
			tool:       "Bash",
			structured: `{"stdout":"","stderr":"","interrupted":true}`,
			want:       "interrupted",
		},
		{
			name:  "non-bash falls back to content size",
			tool:  "Read",
			block: stream.Block{Content: json.RawMessage(`"file contents here"`)},
			want:  "size=18b",
		},
		{
			name: "non-bash with array content sums lengths",
			tool: "Grep",
			block: stream.Block{
				Content: json.RawMessage(`[{"type":"text","text":"hello"},{"type":"text","text":" world"}]`),
			},
			want: "size=11b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatToolResult(tc.tool, tc.block, json.RawMessage(tc.structured))
			if got != tc.want {
				t.Errorf("formatToolResult(%q) = %q, want %q", tc.tool, got, tc.want)
			}
		})
	}
}
