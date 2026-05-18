// Package stream models the JSON event stream emitted by
// `pi -p --mode json` (pi = @earendil-works/pi-coding-agent).
//
// The wire format is newline-delimited JSON: each line is a single
// event object whose "type" field discriminates the payload. The
// stream always terminates in an [AgentEnd] event, after which the pi
// process exits. [Reader] drives a producer of [Event] values from an
// [io.Reader]:
//
//	r := stream.NewReader(stdout)
//	for {
//	    ev, err := r.Next()
//	    if errors.Is(err, io.EOF) {
//	        break
//	    }
//	    switch ev := ev.(type) {
//	    case stream.MessageEnd:    // ...
//	    case stream.AgentEnd:      // terminal; carries the tally + sentinel
//	    case stream.UnknownEvent:  // forward-compat: tally and continue
//	    }
//	}
//
// Decoding is two-pass internally: a small head struct extracts the
// "type" discriminator, then the full line is decoded into the matching
// concrete type. Only the events ralph acts on are decoded into rich
// concrete types ([Session], [MessageEnd], [ToolExecutionStart],
// [ToolExecutionEnd], [AgentEnd], [TurnEnd]); every other recognised pi
// event surfaces as a [KnownEvent] carrier so consumers can tally it
// without this package tracking pi's fast-moving 0.x event set.
// Unrecognised "type" values surface as [UnknownEvent] (paired with
// [ErrUnknownType]); malformed JSON surfaces as a [DecodeError] wrapping
// [ErrMalformed]. Both are recoverable: the scanner stays positioned on
// the next line so decoding resumes.
//
// pi exposes no structured-output / forced-final-answer mechanism, so
// the DONE/CONTINUE control signal is a text sentinel parsed from the
// terminal [AgentEnd] event; see [StatusFromAgentEnd].
package stream

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// Event types produced by pi on its `-p --mode json` output. Only the
// events ralph acts on get a named constant; the known-but-unused kinds
// are matched as string literals in [Reader.Next]'s dispatch.
const (
	TypeSession            = "session"
	TypeMessageEnd         = "message_end"
	TypeToolExecutionStart = "tool_execution_start"
	TypeToolExecutionEnd   = "tool_execution_end"
	TypeTurnEnd            = "turn_end"
	TypeAgentEnd           = "agent_end"
)

// Message roles found in [MessageEnd.Message.Role] and the messages of
// an [AgentEnd]. pi emits a separate "toolResult" message for every
// tool call (in addition to the [ToolExecutionEnd] event); consumers
// use these constants to de-duplicate the tool channel.
const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleToolResult = "toolResult"
)

// Content block types found inside [PiMessage.Content]. The set of
// fields populated on a [ContentBlock] depends on its Type.
const (
	BlockText     = "text"
	BlockThinking = "thinking"
	BlockToolCall = "toolCall"
)

// Status is the terminal control signal ralph derives from the agent's
// final reply. pi has no structured-output mechanism, so the agent is
// instructed to end its last assistant message with a bare line
// `RALPH-STATUS: DONE` or `RALPH-STATUS: CONTINUE`; [StatusFromAgentEnd]
// extracts it from an [AgentEnd].
//
// The zero value [StatusUnknown] is used when the label is absent or
// unrecognised, so callers can distinguish "no parseable sentinel" from
// a committed terminal answer. Note that the loop's safe default for a
// present-but-unparseable sentinel is [StatusContinue], applied by
// [StatusFromAgentEnd], not by this enum.
type Status int

// Status values. [StatusUnknown] is the zero value and prints as the
// empty string so it can be distinguished from a parsed terminal value.
const (
	StatusUnknown Status = iota
	StatusDone
	StatusContinue
)

// Wire-format labels matching the sentinel the build-agent persona
// emits. Callers comparing against parsed values should use the typed
// [Status] constants instead.
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

// ParseStatus maps a sentinel label to its typed [Status]. An empty or
// unrecognised label returns [StatusUnknown] and ok=false.
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

// sentinelRE matches the bare RALPH-STATUS line the build-agent persona
// is instructed to end its final reply with. It is multiline-anchored
// (^/$ match line boundaries) so [StatusFromAgentEnd] can take the LAST
// match across the concatenated text blocks of the final assistant
// message — a stray earlier mention does not win over the real
// terminator.
var sentinelRE = regexp.MustCompile(`(?m)^RALPH-STATUS:[ \t]*(DONE|CONTINUE)[ \t]*$`)

// Buffer bounds for the internal line scanner. The upper bound is
// generous because individual events (notably a tool_execution_end for
// a large read, or an agent_end carrying every message) can be quite
// long; the lower bound is the bufio default.
const (
	scannerInitialBuffer = 64 * 1024
	scannerMaxBuffer     = 16 * 1024 * 1024
)

// Event is the sealed interface satisfied by every value [Reader.Next]
// can return. Callers should type-switch on Event; the closed set is
// [Session], [MessageEnd], [ToolExecutionStart], [ToolExecutionEnd],
// [TurnEnd], [AgentEnd], [KnownEvent], [UnknownEvent].
type Event interface {
	// Kind returns the wire-format "type" discriminator.
	Kind() string
	isStreamEvent()
}

// Sentinel errors returned by [Reader.Next], wrapped in [DecodeError].
var (
	// ErrUnknownType is wrapped when a line's "type" field does not
	// match any pi event kind this package recognises. Reader returns
	// the [UnknownEvent] carrier alongside this error so callers may
	// tally or skip the line at their discretion.
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

// Reader decodes pi's `-p --mode json` line-oriented event stream.
// Reader is not safe for concurrent use.
type Reader struct {
	sc   *bufio.Scanner
	line int
}

// NewReader returns a [Reader] that decodes events from r. The provided
// reader is consumed lazily, one line per call to [Reader.Next].
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, scannerInitialBuffer), scannerMaxBuffer)
	return &Reader{sc: sc}
}

// Line returns the 1-based line number of the most recently returned
// event or error. Useful for log messages and diagnostic output.
func (r *Reader) Line() int { return r.line }

// knownButUnused is the set of pi event types ralph recognises but does
// not deep-decode: they are tallied via the [KnownEvent] carrier, never
// rendered. Listing them explicitly keeps a genuinely novel pi event
// distinguishable from a known-noisy one — a new type still becomes an
// [UnknownEvent] so format drift is observable.
var knownButUnused = map[string]struct{}{
	"agent_start":           {},
	"turn_start":            {},
	"message_start":         {},
	"message_update":        {},
	"tool_execution_update": {},
	"compaction_start":      {},
	"compaction_end":        {},
	"auto_retry_start":      {},
	"auto_retry_end":        {},
	"queue_update":          {},
	"extension_ui_request":  {},
	"extension_error":       {},
}

// Next returns the next decoded event from the stream. At end of stream
// Next returns ([io.EOF]). The stream package never fabricates an
// [AgentEnd]: a stream that ends without one simply reaches io.EOF
// (consumers turn the missing-terminal case into an iteration error).
//
// On malformed JSON or an unexpected payload shape Next returns a
// wrapped [*DecodeError] (matching [ErrMalformed] via errors.Is) and a
// nil event. On a JSON object whose "type" is unrecognised Next returns
// the [UnknownEvent] carrier alongside a [*DecodeError] wrapping
// [ErrUnknownType]. In every error case the underlying stream remains
// positioned on the next line, so callers can resume.
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
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: %w", ErrMalformed, err)}
	}

	switch head.Type {
	case TypeSession:
		return decodeConcrete[Session](r, line, "session")
	case TypeMessageEnd:
		return decodeConcrete[MessageEnd](r, line, "message_end")
	case TypeToolExecutionStart:
		return decodeConcrete[ToolExecutionStart](r, line, "tool_execution_start")
	case TypeToolExecutionEnd:
		return decodeConcrete[ToolExecutionEnd](r, line, "tool_execution_end")
	case TypeTurnEnd:
		return decodeConcrete[TurnEnd](r, line, "turn_end")
	case TypeAgentEnd:
		return decodeConcrete[AgentEnd](r, line, "agent_end")
	default:
		// Copy the line: the scanner's slice is overwritten on the
		// next Scan and the carrier is meant to outlive that.
		payload := append(json.RawMessage(nil), line...)
		if _, ok := knownButUnused[head.Type]; ok {
			// Recognised but intentionally not deep-decoded. No error:
			// consumers tally it like any other event.
			return KnownEvent{Type: head.Type, Payload: payload}, nil
		}
		ev := UnknownEvent{Type: head.Type, Payload: payload}
		return ev, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: %q", ErrUnknownType, head.Type)}
	}
}

// decodeConcrete is the second pass of the two-pass decode: the head
// has already routed on "type"; here the full line is unmarshalled into
// the concrete event type T. A shape mismatch becomes a recoverable
// [DecodeError] wrapping [ErrMalformed].
func decodeConcrete[T Event](r *Reader, line []byte, what string) (Event, error) {
	var ev T
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, &DecodeError{Line: r.line, Bytes: line, Err: fmt.Errorf("%w: %s: %w", ErrMalformed, what, err)}
	}
	return ev, nil
}

// Session is the first event of every pi run: it announces the protocol
// version, a session UUID, the wall-clock timestamp, and pi's cwd.
type Session struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

// Kind reports the wire-format discriminator for a [Session] event.
func (Session) Kind() string   { return TypeSession }
func (Session) isStreamEvent() {}

// MessageEnd is emitted once per settled message. Role discriminates
// the payload: an assistant message carries the LLM accounting fields
// ([Usage], Provider, Model, etc.); a user message carries only content
// and timestamp; pi also emits a "toolResult" message_end for every
// tool call, which consumers ignore in favour of the
// tool_execution_* channel (see [ToolExecutionStart]/[ToolExecutionEnd]).
type MessageEnd struct {
	Message PiMessage `json:"message"`
}

// Kind reports the wire-format discriminator for a [MessageEnd] event.
func (MessageEnd) Kind() string   { return TypeMessageEnd }
func (MessageEnd) isStreamEvent() {}

// PiMessage is pi's message shape, shared by [MessageEnd] and the
// elements of [AgentEnd.Messages]. Only the assistant role populates
// the LLM-accounting fields (Usage/Provider/Model/ResponseModel/API/
// StopReason). The toolResult role additionally populates ToolCallID,
// ToolName and IsError; consumers de-duplicate the tool channel by
// ignoring toolResult messages here.
type PiMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`

	// LLM-accounting fields, present only on assistant messages.
	API string `json:"api,omitempty"`
	// Provider is the pi provider id (e.g. "openai-codex").
	Provider string `json:"provider,omitempty"`
	// Model is the requested model id.
	Model string `json:"model,omitempty"`
	// ResponseModel is the model the provider actually served the
	// request with; often absent, in which case callers fall back to
	// Model (the Q6 "effective model").
	ResponseModel string `json:"responseModel,omitempty"`
	// Usage is the per-message, per-turn token/cost accounting. It is
	// NOT cumulative; the run total is the sum over assistant messages.
	Usage *Usage `json:"usage,omitempty"`
	// StopReason is one of stop|length|toolUse|error|aborted.
	StopReason string `json:"stopReason,omitempty"`
	// ResponseID is the provider's response identifier (diagnostic).
	ResponseID string `json:"responseId,omitempty"`
	// Timestamp is pi's unix-millis stamp for the message.
	Timestamp int64 `json:"timestamp,omitempty"`

	// toolResult-only fields. pi emits a separate toolResult message
	// alongside the tool_execution_end event for the same call; these
	// let consumers recognise and skip it.
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
}

// ContentBlock is one element of a [PiMessage.Content] array. The set
// of populated fields depends on Type; see the Block* constants. A
// toolCall block carries Arguments verbatim as [json.RawMessage] so
// tool-specific decoding stays a render-package concern.
type ContentBlock struct {
	Type string `json:"type"`
	// Text is set on a "text" block.
	Text string `json:"text,omitempty"`
	// Thinking is set on a "thinking" block.
	Thinking string `json:"thinking,omitempty"`
	// ID, Name and Arguments are set on a "toolCall" block. Arguments
	// is left raw on purpose.
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolExecutionStart marks pi beginning a tool invocation. Args is the
// tool-specific argument object left raw (e.g. {"path":…} for read,
// {"path":…,"edits":[…]} for edit) so the render package owns
// tool-shaped decoding.
type ToolExecutionStart struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
}

// Kind reports the wire-format discriminator for a [ToolExecutionStart].
func (ToolExecutionStart) Kind() string   { return TypeToolExecutionStart }
func (ToolExecutionStart) isStreamEvent() {}

// ToolExecutionEnd marks pi finishing a tool invocation. Result is left
// raw (it carries {"content":[…],"details":{…}} whose shape is
// tool-specific) so the render package owns decoding. IsError reports
// whether the tool reported a failure. This event, paired with
// [ToolExecutionStart] by ToolCallID, is the sole tool channel
// consumers should render and count (pi also emits a redundant
// toolResult [MessageEnd] for the same call).
type ToolExecutionEnd struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Result     json.RawMessage `json:"result"`
	IsError    bool            `json:"isError"`
}

// Kind reports the wire-format discriminator for a [ToolExecutionEnd].
func (ToolExecutionEnd) Kind() string   { return TypeToolExecutionEnd }
func (ToolExecutionEnd) isStreamEvent() {}

// TurnEnd marks the end of one assistant turn. It carries the turn's
// final assistant message and any tool results produced in the turn.
// ralph treats it as an optional boundary marker; the authoritative
// per-iteration tally comes from [AgentEnd].
type TurnEnd struct {
	Message     PiMessage         `json:"message"`
	ToolResults []json.RawMessage `json:"toolResults,omitempty"`
}

// Kind reports the wire-format discriminator for a [TurnEnd] event.
func (TurnEnd) Kind() string   { return TypeTurnEnd }
func (TurnEnd) isStreamEvent() {}

// AgentEnd is the terminal event: pi emits it once, then the process
// exits. Messages is the full conversation transcript for the run; it
// is the single source of truth for the per-iteration token/cost tally
// (sum [Usage] over assistant messages, which are per-turn not
// cumulative) and for the DONE/CONTINUE sentinel
// (see [StatusFromAgentEnd]).
type AgentEnd struct {
	Messages []PiMessage `json:"messages"`
}

// Kind reports the wire-format discriminator for an [AgentEnd] event.
func (AgentEnd) Kind() string   { return TypeAgentEnd }
func (AgentEnd) isStreamEvent() {}

// KnownEvent carries a pi event ralph recognises but intentionally does
// not deep-decode (agent_start, turn_start, message_start,
// message_update, tool_execution_update, compaction_*, auto_retry_*,
// queue_update, extension_*). It is returned with a nil error so
// consumers tally it like any other event; the raw payload is preserved
// for diagnostics. Truly novel pi types become an [UnknownEvent]
// instead, keeping format drift observable.
type KnownEvent struct {
	Type    string
	Payload json.RawMessage
}

// Kind reports the wire-format "type" preserved from the line.
func (e KnownEvent) Kind() string { return e.Type }
func (KnownEvent) isStreamEvent() {}

// UnknownEvent carries any line whose "type" field matches no pi event
// kind this package recognises, so callers can log or forward it
// without crashing. It is returned paired with a [DecodeError] wrapping
// [ErrUnknownType].
type UnknownEvent struct {
	Type    string
	Payload json.RawMessage
}

// Kind reports the wire-format "type" preserved from the line.
func (e UnknownEvent) Kind() string { return e.Type }
func (UnknownEvent) isStreamEvent() {}

// Usage is pi's per-message, per-turn token/cost accounting. It is NOT
// cumulative: the per-iteration figure is the sum over the assistant
// messages of an [AgentEnd]. Cost is real fractional USD that pi
// computes itself, per provider; pi's number is authoritative (ralph no
// longer carries a pricing table).
type Usage struct {
	Input       int  `json:"input"`
	Output      int  `json:"output"`
	CacheRead   int  `json:"cacheRead"`
	CacheWrite  int  `json:"cacheWrite"`
	TotalTokens int  `json:"totalTokens"`
	Cost        Cost `json:"cost"`
}

// Cost is the per-message USD cost breakdown nested inside [Usage]. All
// fields are fractional dollars as reported by pi.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// StatusFromAgentEnd extracts the DONE/CONTINUE control sentinel from a
// terminal [AgentEnd], implementing the locked Q3 contract:
//
//   - take the LAST message with role "assistant";
//   - concatenate its "text" content blocks (thinking/toolCall blocks
//     are ignored);
//   - find the LAST line matching `^RALPH-STATUS:\s*(DONE|CONTINUE)\s*$`
//     (multiline) and map DONE→[StatusDone], CONTINUE→[StatusContinue].
//
// If no assistant message or no parseable sentinel is present the safe
// default is [StatusContinue] — a missed signal costs at most one extra
// fresh iteration, never a premature DONE. (The "no agent_end at all"
// case is the consumers' concern: this package never fabricates one.)
func StatusFromAgentEnd(ev AgentEnd) Status {
	// Find the last assistant message.
	last := -1
	for i := range ev.Messages {
		if ev.Messages[i].Role == RoleAssistant {
			last = i
		}
	}
	if last < 0 {
		return StatusContinue
	}

	// Concatenate the text blocks of that message. pi emits each block
	// as a discrete element; the sentinel may sit at the end of a block
	// or be its own block, so a plain join with newlines between blocks
	// preserves the per-line anchoring the regex relies on.
	var text string
	for _, b := range ev.Messages[last].Content {
		if b.Type != BlockText {
			continue
		}
		if text != "" {
			text += "\n"
		}
		text += b.Text
	}

	matches := sentinelRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return StatusContinue
	}
	if s, ok := ParseStatus(matches[len(matches)-1][1]); ok {
		return s
	}
	return StatusContinue
}
