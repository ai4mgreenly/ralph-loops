package render

import (
	"encoding/json"
	"strings"
)

// paramTruncate is the maximum width of a tool-call primary-argument
// value before it is shortened with an ellipsis, so a header stays one
// readable line even when the agent passes a long command or path.
const paramTruncate = 60

// toolEdit is pi's `edit` tool name. It is the only tool that gets
// special rendering (a reconstructed diff); every other tool flows
// through the single generic renderer. Held as a constant so the
// dispatch in [Emitter.onToolEnd] reads intentionally.
const toolEdit = "edit"

// primaryArgKeys is the ordered set of argument keys the B-lite header
// surfaces, first-present-wins. pi's tools key their salient input on
// one of these: `path` (read/edit/write/ls), `command` (bash), or
// `pattern` (grep/find). The order encodes precedence when a tool
// happens to carry more than one.
var primaryArgKeys = []string{"path", "command", "pattern"}

// primaryArg returns the B-lite header suffix for a
// [stream.ToolExecutionStart]'s args: the value of the first present
// key among path/command/pattern, with whitespace collapsed to single
// spaces and the result truncated to [paramTruncate] runes. Returns the
// empty string when args is absent, unparseable, or carries none of the
// primary keys (the header then degrades to a bare tool name rather
// than crash).
func primaryArg(rawArgs json.RawMessage) string {
	if len(rawArgs) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rawArgs, &m); err != nil {
		return ""
	}
	for _, k := range primaryArgKeys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			// Non-string primary arg (unexpected); fall back to its raw
			// JSON so the operator still sees something was passed.
			s = string(raw)
		}
		return truncateRunes(collapseWhitespace(s), paramTruncate)
	}
	return ""
}

// piResult is pi's tool-result envelope:
// `{"content":[{"type":"text","text":"…"}], "details":{…optional…}}`.
// Only the text content is surfaced by the generic renderer; details
// are tool-specific and intentionally not interpreted (the locked
// B-lite decision renders the text channel alone).
type piResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// resultContentText concatenates the text of every content element in
// a pi tool result. A non-text element contributes nothing. An absent
// or unparseable result yields the empty string so the renderer can
// fall through to "no body" rather than panic on malformed input.
func resultContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var r piResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type != "" && c.Type != "text" {
			continue
		}
		b.WriteString(c.Text)
	}
	return b.String()
}

// collapseWhitespace replaces every run of whitespace with a single
// space and trims the result, so a multi-line tool argument becomes one
// clean header line.
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

// truncateRunes shortens s to at most limit runes, replacing the last
// retained rune with an ellipsis when truncation occurs. Operates on
// runes, not bytes, so multi-byte input is never split mid-character.
func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}
