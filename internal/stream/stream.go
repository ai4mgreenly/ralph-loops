// Package stream models the JSON event stream emitted by
// `claude -p --output-format stream-json`.
//
// Only the fields ralph actually consumes are typed. Each line on the
// stream is first decoded into a [RawEvent] for type-routing, then
// re-decoded into the matching concrete type. This two-pass approach
// keeps the type discriminator separate from the payload while still
// letting Go's encoding/json validate field shapes.
package stream

import "encoding/json"

// Event types produced by the claude CLI on its JSON-line output.
const (
	TypeAssistant = "assistant"
	TypeUser      = "user"
	TypeResult    = "result"
	TypeSystem    = "system"
	TypeRateLimit = "rate_limit_event"
)

// Block content types found inside [Message.Content].
const (
	BlockText             = "text"
	BlockThinking         = "thinking"
	BlockRedactedThinking = "redacted_thinking"
	BlockToolUse          = "tool_use"
	BlockToolResult       = "tool_result"
)

// Status values the claude CLI is constrained to return via [SchemaJSON].
const (
	StatusDone     = "DONE"
	StatusContinue = "CONTINUE"
)

// SchemaJSON is the JSON Schema passed to claude via --json-schema.
// It forces the model to emit exactly {"status":"DONE"} or
// {"status":"CONTINUE"} as its structured output.
const SchemaJSON = `{"type":"object","properties":{"status":{"type":"string","enum":["DONE","CONTINUE"]}},"required":["status"]}`

// RawEvent captures the discriminator fields needed to dispatch a stream
// line to its concrete type. The rest of the payload is preserved in
// [RawEvent.Payload] for a second decode pass.
type RawEvent struct {
	Type    string
	Subtype string
	Payload json.RawMessage
}

// UnmarshalJSON decodes the discriminator while keeping the original
// bytes around for follow-up decoding.
func (e *RawEvent) UnmarshalJSON(data []byte) error {
	var head struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	e.Type = head.Type
	e.Subtype = head.Subtype
	e.Payload = append(e.Payload[:0], data...)
	return nil
}

// Assistant is the payload of an assistant event.
type Assistant struct {
	Message Message `json:"message"`
}

// User is the payload of a user (tool result or replayed input) event.
type User struct {
	Message       Message         `json:"message"`
	ToolUseResult json.RawMessage `json:"tool_use_result,omitempty"`
}

// Result is the payload of the terminal result event for an iteration.
type Result struct {
	NumTurns         int             `json:"num_turns,omitempty"`
	DurationMS       int             `json:"duration_ms,omitempty"`
	TotalCostUSD     float64         `json:"total_cost_usd,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	Usage            *Usage          `json:"usage,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
}

// System is the payload of a system event (session start, tool list,
// permission mode, etc.).
type System struct {
	Subtype        string   `json:"subtype,omitempty"`
	Model          string   `json:"model,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	PermissionMode string   `json:"permissionMode,omitempty"`
	Tools          []string `json:"tools,omitempty"`
}

// RateLimit is the payload of a rate_limit_event.
type RateLimit struct {
	Info *RateLimitInfo `json:"rate_limit_info,omitempty"`
}

// RateLimitInfo describes the rate-limit state at the moment the event
// was produced.
type RateLimitInfo struct {
	RateLimitType  string  `json:"rateLimitType,omitempty"`
	Status         string  `json:"status,omitempty"`
	Utilization    float64 `json:"utilization,omitempty"`
	ResetsAt       int64   `json:"resetsAt,omitempty"`
	IsUsingOverage bool    `json:"isUsingOverage,omitempty"`
}

// Message is the role-and-content pair found inside assistant and user
// events.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Block is one element of a [Message.Content] array. The set of fields
// populated depends on [Block.Type]; see the Block* constants.
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Name      string          `json:"name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// Usage is the per-iteration token accounting reported on a result event.
type Usage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// StatusOutput is the schema-constrained reply ralph forces from claude
// at the end of every iteration.
type StatusOutput struct {
	Status string `json:"status"`
}
