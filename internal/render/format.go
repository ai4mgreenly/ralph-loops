package render

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// paramTruncate is the maximum width of a tool-call parameter value
// before it is shortened with an ellipsis. The Ruby driver uses 60.
const paramTruncate = 60

// formatToolCallParam summarises one tool's input arguments for the
// activity log. Each tool has a "primary" key worth showing in line —
// for Bash that's command, for Read/Write/Edit it's the file path,
// for Glob/Grep it's the pattern. For unknown tools the first key in
// the input object is used.
//
// The result is either an empty string (no input) or "key=\"value\"",
// with whitespace collapsed and over-long values truncated.
func formatToolCallParam(name string, rawInput json.RawMessage) string {
	input := decodeObject(rawInput)
	if len(input) == 0 {
		return ""
	}

	key := primaryParamKey(name, input)
	val, ok := input[key]
	if !ok {
		// The selected key isn't present (e.g. a Bash call with no
		// command field). Surface the first key (in sorted order, so
		// output stays deterministic) with a placeholder so the
		// operator at least sees something was passed.
		if k := firstSortedKey(input); k != "" {
			return fmt.Sprintf("%s=…", k)
		}
		return ""
	}

	return fmt.Sprintf("%s=%s", key, strconv.Quote(truncate(collapseWhitespace(stringify(val)), paramTruncate)))
}

// primaryParamKey returns the input field worth showing in line for
// tool name. Falls back to the first key in input (sorted, so the
// fallback is deterministic) if the tool is unknown.
func primaryParamKey(name string, input map[string]any) string {
	switch name {
	case "Bash":
		return "command"
	case "Read", "Write", "Edit", "NotebookEdit":
		return "file_path"
	case "Glob", "Grep":
		return "pattern"
	}
	return firstSortedKey(input)
}

// firstSortedKey returns the lexicographically smallest key of m, or
// "" when m is empty. Used by the formatter to make "first key" fallbacks
// deterministic — Go's map iteration order is randomized, so a naïve
// `for k := range m { return k }` made formatter output flap between
// equivalent inputs.
func firstSortedKey(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

// formatToolResult summarises a tool result. For Bash calls the
// claude CLI ships a parsed `tool_use_result` object alongside the
// content array; we surface stdout/stderr byte counts and the
// interrupted flag from there. For every other tool we fall back to
// the size of the content array.
func formatToolResult(name string, block stream.Block, structured json.RawMessage) string {
	if name == "Bash" && len(structured) > 0 {
		var s struct {
			Stdout      string `json:"stdout"`
			Stderr      string `json:"stderr"`
			Interrupted bool   `json:"interrupted"`
		}
		if err := json.Unmarshal(structured, &s); err == nil {
			var bits []string
			if n := len(s.Stdout); n > 0 {
				bits = append(bits, "stdout="+ui.FormatBytes(n))
			}
			if n := len(s.Stderr); n > 0 {
				bits = append(bits, "stderr="+ui.FormatBytes(n))
			}
			if s.Interrupted {
				bits = append(bits, "interrupted")
			}
			return strings.Join(bits, " ")
		}
	}
	return "size=" + ui.FormatBytes(contentBytes(block))
}

// contentBytes returns the total UTF-8 byte length of all text in a
// tool-result block's content. The content is either a JSON string or
// a JSON array of {type, text} objects; both are tolerated.
func contentBytes(block stream.Block) int {
	if len(block.Content) == 0 {
		return 0
	}
	var s string
	if err := json.Unmarshal(block.Content, &s); err == nil {
		return len(s)
	}
	var arr []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(block.Content, &arr); err == nil {
		total := 0
		for _, item := range arr {
			total += len(item.Text)
		}
		return total
	}
	return 0
}

// decodeObject best-effort decodes a JSON object into a map. Returns
// an empty map on any failure so callers can iterate without nil
// checks.
func decodeObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// stringify returns a string representation of an arbitrary JSON
// value. Strings come through unquoted; everything else is rendered
// via the default Sprint formatting.
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// collapseWhitespace replaces every run of whitespace with a single
// space and trims the result. Mirrors the Ruby driver's `gsub(/\s+/,
// ' ').strip` so multi-line tool inputs become one clean line.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // collapses leading whitespace too
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// truncate shortens s to at most max runes, replacing the last rune
// with an ellipsis when truncation occurs. The Ruby driver uses 60
// chars + a single Unicode ellipsis byte, which happens to align
// here too.
func truncate(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
