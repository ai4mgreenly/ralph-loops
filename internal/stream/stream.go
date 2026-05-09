// Package stream models the JSON event stream emitted by
// `claude -p --output-format stream-json`.
//
// The wire format is newline-delimited JSON: each line is a single
// event object whose "type" field discriminates the payload. [Reader]
// drives a producer of [Event] values from an [io.Reader]:
//
//	r := stream.NewReader(stdout)
//	for {
//	    ev, err := r.Next()
//	    if errors.Is(err, io.EOF) {
//	        break
//	    }
//	    switch ev := ev.(type) {
//	    case stream.Assistant: // ...
//	    case stream.Result:    // ...
//	    case stream.UnknownEvent:
//	        // forward-compat: log and continue
//	    }
//	}
//
// Decoding is two-pass internally: a small head struct extracts the
// discriminator, then the full line is decoded into the matching
// concrete type. Unrecognized "type" values surface as [UnknownEvent]
// (paired with [ErrUnknownType]) rather than being silently dropped,
// so the system stays safe when claude introduces a new event kind.
package stream

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

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

// Status is the schema-constrained status the claude CLI returns at the
// end of every iteration. The wire form is a string ("DONE" / "CONTINUE");
// callers receive a typed value from [ParseStatus] and can compare with
// the named constants below.
//
// The zero value [StatusUnknown] is used when a status field is absent
// or carries an unrecognised label, so callers can distinguish "the
// agent did not commit to a terminal answer" from a successful parse.
type Status int

// Status values. [StatusUnknown] is the zero value and prints as the
// empty string so it can be distinguished from a parsed terminal value.
const (
	StatusUnknown Status = iota
	StatusDone
	StatusContinue
)

// Wire-format labels matching the JSON schema enum values. Used to
// drive [ParseStatus] and to assemble [SchemaJSON]; callers comparing
// against parsed values should use the typed [Status] constants instead.
const (
	wireStatusDone     = "DONE"
	wireStatusContinue = "CONTINUE"
)

// String returns the wire-format label for s. [StatusUnknown] renders
// as the empty string.
func (s Status) String() string {
	switch s {
	case StatusDone:
		return wireStatusDone
	case StatusContinue:
		return wireStatusContinue
	default:
		return ""
	}
}

// ParseStatus maps a wire-format status label to its typed [Status].
// An empty or unrecognised label returns [StatusUnknown] and ok=false.
func ParseStatus(s string) (Status, bool) {
	switch s {
	case wireStatusDone:
		return StatusDone, true
	case wireStatusContinue:
		return StatusContinue, true
	default:
		return StatusUnknown, false
	}
}

// Tool names as they appear in the `name` field of a tool_use block
// (or the `tools` list of a `system` event). Only tools the codebase
// dispatches on by name are listed here; new tools can be matched as
// string literals until a renderer or stats path needs to special-case
// them.
const (
	ToolBash         = "Bash"
	ToolRead         = "Read"
	ToolEdit         = "Edit"
	ToolWrite        = "Write"
	ToolGlob         = "Glob"
	ToolGrep         = "Grep"
	ToolNotebookEdit = "NotebookEdit"
)

// SchemaJSON is the JSON Schema passed to claude via --json-schema.
// It forces the model to emit exactly {"status":"DONE"} or
// {"status":"CONTINUE"} as its structured output. The enum values are
// the same wire-format labels surfaced through [Status.String].
const SchemaJSON = `{"type":"object","properties":{"status":{"type":"string","enum":["DONE","CONTINUE"]}},"required":["status"]}`

// Buffer bounds for the internal line scanner. The upper bound is
// generous because individual events (notably tool_use_result for a
// large Read) can be quite long; the lower bound is the bufio default.
const (
	scannerInitialBuffer = 64 * 1024
	scannerMaxBuffer     = 16 * 1024 * 1024
)

// Event is the sealed interface satisfied by every value [Reader.Next]
// can return. Callers should type-switch on Event; the closed set is:
// [Assistant], [User], [Result], [System], [RateLimit], [UnknownEvent].
type Event interface {
	// Kind returns the wire-format discriminator (one of TypeXxx).
	Kind() string
	isStreamEvent()
}

// Sentinel errors returned by [Reader.Next], wrapped in [DecodeError].
var (
	// ErrUnknownType is wrapped when a line's "type" field does not
	// match a known constant. Reader returns the [UnknownEvent] carrier
	// alongside this error so callers may surface or skip the line at
	// their discretion.
	ErrUnknownType = errors.New("stream: unknown event type")
	// ErrMalformed is wrapped when a line cannot be parsed as JSON or
	// its payload does not match the expected shape for its type.
	ErrMalformed = errors.New("stream: malformed event")
)

// DecodeError reports a per-line decoding failure with enough context
// (line number, raw bytes) for callers to surface or recover from it.
// Wrapped errors include [ErrUnknownType] or [ErrMalformed].
type DecodeError struct {
	// Line is the 1-based index of the offending line in the stream.
	Line int
	// Bytes is the raw line that failed to decode. Owned by the
	// scanner; copy if retained beyond the next call to [Reader.Next].
	Bytes []byte
	// Err is the underlying cause, wrappable with errors.Is/As.
	Err error
}

// Error implements [error] and reports the line number alongside the
// wrapped cause.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("stream: line %d: %v", e.Line, e.Err)
}

// Unwrap returns the wrapped cause so callers can use [errors.Is] and
// [errors.As] against [ErrUnknownType] or [ErrMalformed].
func (e *DecodeError) Unwrap() error { return e.Err }

// Reader decodes the claude stream-json line-oriented event stream.
// Reader is not safe for concurrent use.
type Reader struct {
	sc   *bufio.Scanner
	line int
}

// NewReader returns a [Reader] that decodes events from r. The
// provided reader is consumed lazily, one line per call to
// [Reader.Next].
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, scannerInitialBuffer), scannerMaxBuffer)
	return &Reader{sc: sc}
}

// Line returns the 1-based line number of the most recently returned
// event or error. Useful for log messages and diagnostic output.
func (r *Reader) Line() int { return r.line }

// Next returns the next decoded event from the stream. At end of
// stream Next returns ([io.EOF]).
//
// On malformed JSON or unexpected payload shape Next returns a wrapped
// [*DecodeError] (matching [ErrMalformed] via errors.Is). On a known
// JSON object whose "type" is unrecognized Next returns the carrier
// [UnknownEvent] alongside a [*DecodeError] wrapping [ErrUnknownType];
// callers may inspect or ignore both. In every case the underlying
// stream remains positioned on the next line, so callers can resume.
func (r *Reader) Next() (Event, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return nil, fmt.Errorf("stream: read: %w", err)
		}
		return nil, io.EOF
	}
	r.line++
	line := r.sc.Bytes()

	var head struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: %w", ErrMalformed, err)}
	}

	switch head.Type {
	case TypeAssistant:
		var ev Assistant
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: assistant: %w", ErrMalformed, err)}
		}
		return ev, nil
	case TypeUser:
		var ev User
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: user: %w", ErrMalformed, err)}
		}
		return ev, nil
	case TypeResult:
		var ev Result
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: result: %w", ErrMalformed, err)}
		}
		return ev, nil
	case TypeSystem:
		var ev System
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: system: %w", ErrMalformed, err)}
		}
		return ev, nil
	case TypeRateLimit:
		var ev RateLimit
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: rate_limit: %w", ErrMalformed, err)}
		}
		return ev, nil
	default:
		// Preserve the raw payload so callers can log or forward it.
		// Copy the line bytes; the scanner's slice is overwritten on
		// the next Scan.
		payload := append(json.RawMessage(nil), line...)
		ev := UnknownEvent{Type: head.Type, Subtype: head.Subtype, Payload: payload}
		return ev, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: %q", ErrUnknownType, head.Type)}
	}
}

// Assistant is the payload of an assistant event.
type Assistant struct {
	Message Message `json:"message"`
}

// Kind reports the wire-format discriminator for an [Assistant] event.
func (Assistant) Kind() string   { return TypeAssistant }
func (Assistant) isStreamEvent() {}

// User is the payload of a user (tool result or replayed input) event.
type User struct {
	Message       Message         `json:"message"`
	ToolUseResult json.RawMessage `json:"tool_use_result,omitempty"`
}

// Kind reports the wire-format discriminator for a [User] event.
func (User) Kind() string   { return TypeUser }
func (User) isStreamEvent() {}

// Result is the payload of the terminal result event for an iteration.
type Result struct {
	NumTurns         int             `json:"num_turns,omitempty"`
	DurationMS       int             `json:"duration_ms,omitempty"`
	TotalCostUSD     float64         `json:"total_cost_usd,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	Usage            *Usage          `json:"usage,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
}

// Kind reports the wire-format discriminator for a [Result] event.
func (Result) Kind() string   { return TypeResult }
func (Result) isStreamEvent() {}

// System is the payload of a system event (session start, tool list,
// permission mode, etc.).
type System struct {
	Subtype        string   `json:"subtype,omitempty"`
	Model          string   `json:"model,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	PermissionMode string   `json:"permissionMode,omitempty"`
	Tools          []string `json:"tools,omitempty"`
}

// Kind reports the wire-format discriminator for a [System] event.
func (System) Kind() string   { return TypeSystem }
func (System) isStreamEvent() {}

// RateLimit is the payload of a rate_limit_event.
type RateLimit struct {
	Info *RateLimitInfo `json:"rate_limit_info,omitempty"`
}

// Kind reports the wire-format discriminator for a [RateLimit] event.
func (RateLimit) Kind() string   { return TypeRateLimit }
func (RateLimit) isStreamEvent() {}

// UnknownEvent carries any line whose "type" field does not match a
// known event kind, allowing callers to log or forward without
// crashing. The Type field tracks the wire discriminator.
type UnknownEvent struct {
	Type    string
	Subtype string
	Payload json.RawMessage
}

// Kind reports the wire-format discriminator preserved from the
// originating line. For [UnknownEvent] the value is whatever literal
// "type" the wire carried.
func (e UnknownEvent) Kind() string { return e.Type }
func (UnknownEvent) isStreamEvent() {}

// RateLimitInfo describes the rate-limit state at the moment the event
// was produced. Field names use camelCase to match claude's wire
// format (the rest of the schema is snake_case).
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
// at the end of every iteration. The Status field carries the wire-format
// label; use [ParseStatus] to convert to the typed [Status] enum.
type StatusOutput struct {
	Status string `json:"status"`
}

// userMessageBlock is one content block inside an outgoing user message.
// The only kind ralph emits is "text".
type userMessageBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// userMessagePayload is the inner Message field of a user-message
// envelope sent on the agent's stdin.
type userMessagePayload struct {
	Role    string             `json:"role"`
	Content []userMessageBlock `json:"content"`
}

// userMessage is the wire-format envelope for a single stream-json user
// message line written to the agent's stdin.
type userMessage struct {
	Type    string             `json:"type"`
	Message userMessagePayload `json:"message"`
}

// WriteUserMessage writes a single stream-json user-message envelope
// containing text to w, terminated with the newline that the protocol's
// line framing requires. Returns wrapped errors if marshaling or writing
// fails.
func WriteUserMessage(w io.Writer, text string) error {
	msg := userMessage{
		Type: "user",
		Message: userMessagePayload{
			Role:    "user",
			Content: []userMessageBlock{{Type: "text", Text: text}},
		},
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal user message: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("write user message: %w", err)
	}
	return nil
}
