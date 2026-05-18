package stream_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// readFixture loads a real pi `-p --mode json` capture from testdata.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// drain reads every event from r until io.EOF, returning the events in
// order and the count of decode errors observed (unknown-type carriers
// are returned with a paired error and still appear in the slice).
func drain(t *testing.T, r *stream.Reader) (events []stream.Event, decodeErrs int) {
	t.Helper()
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return events, decodeErrs
		}
		if err != nil {
			decodeErrs++
			var de *stream.DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("non-DecodeError from Next: %v", err)
			}
			if ev != nil {
				// Unknown-type carrier paired with an error.
				events = append(events, ev)
			}
			continue
		}
		if ev == nil {
			t.Fatalf("nil event with nil error")
		}
		events = append(events, ev)
	}
}

// TestReader_DispatchesAllActedOnTypes replays the real tool-edit
// capture and asserts that every acted-on pi event type decodes to its
// concrete Go type, that known-but-unused types arrive as KnownEvent
// carriers, and that the stream ends cleanly at io.EOF with exactly one
// terminal AgentEnd. Per Q14(c) this asserts decoded STRUCTURE, not
// byte-identical output (the fixture carries volatile ids/timestamps).
func TestReader_DispatchesAllActedOnTypes(t *testing.T) {
	t.Parallel()

	r := stream.NewReader(bytes.NewReader(readFixture(t, "tool-edit.jsonl")))
	events, decodeErrs := drain(t, r)

	if decodeErrs != 0 {
		t.Errorf("unexpected decode errors: %d", decodeErrs)
	}

	var (
		sessions, msgEnds, turnEnds, agentEnds int
		toolStarts, toolEnds, knowns           int
	)
	for _, ev := range events {
		switch ev.(type) {
		case stream.Session:
			sessions++
		case stream.MessageEnd:
			msgEnds++
		case stream.ToolExecutionStart:
			toolStarts++
		case stream.ToolExecutionEnd:
			toolEnds++
		case stream.TurnEnd:
			turnEnds++
		case stream.AgentEnd:
			agentEnds++
		case stream.KnownEvent:
			knowns++
		default:
			t.Errorf("unexpected event type %T", ev)
		}
	}

	if sessions != 1 {
		t.Errorf("Session count = %d, want 1", sessions)
	}
	if msgEnds != 4 {
		t.Errorf("MessageEnd count = %d, want 4", msgEnds)
	}
	if toolStarts != 1 || toolEnds != 1 {
		t.Errorf("tool exec counts = %d/%d, want 1/1", toolStarts, toolEnds)
	}
	if turnEnds != 2 {
		t.Errorf("TurnEnd count = %d, want 2", turnEnds)
	}
	if agentEnds != 1 {
		t.Errorf("AgentEnd count = %d, want exactly 1 (terminal)", agentEnds)
	}
	if knowns == 0 {
		t.Errorf("expected KnownEvent carriers for known-but-unused types, got 0")
	}
	// The terminal event must be the AgentEnd.
	if _, ok := events[len(events)-1].(stream.AgentEnd); !ok {
		t.Errorf("last event = %T, want stream.AgentEnd", events[len(events)-1])
	}
}

// TestReader_SessionShape pins the first event's concrete fields.
func TestReader_SessionShape(t *testing.T) {
	t.Parallel()
	r := stream.NewReader(bytes.NewReader(readFixture(t, "done.jsonl")))
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	s, ok := ev.(stream.Session)
	if !ok {
		t.Fatalf("first event = %T, want stream.Session", ev)
	}
	if s.Version == 0 || s.ID == "" || s.Timestamp == "" || s.Cwd == "" {
		t.Errorf("Session under-populated: %+v", s)
	}
	if r.Line() != 1 {
		t.Errorf("Line = %d, want 1", r.Line())
	}
}

// TestReader_AssistantMessageEndCarriesUsage asserts the assistant
// message_end exposes the LLM-accounting fields with pi's Usage shape,
// and that a user message_end does not. The role discriminator must be
// visible so consumers can de-dupe the tool channel.
func TestReader_AssistantMessageEndCarriesUsage(t *testing.T) {
	t.Parallel()
	r := stream.NewReader(bytes.NewReader(readFixture(t, "done.jsonl")))
	events, _ := drain(t, r)

	var sawAssistant, sawUser bool
	for _, ev := range events {
		me, ok := ev.(stream.MessageEnd)
		if !ok {
			continue
		}
		switch me.Message.Role {
		case stream.RoleAssistant:
			sawAssistant = true
			if me.Message.Usage == nil {
				t.Fatalf("assistant message_end missing Usage")
			}
			u := me.Message.Usage
			if u.TotalTokens != u.Input+u.Output+u.CacheRead+u.CacheWrite {
				// Not a hard rule for every provider, but for the codex
				// fixture totalTokens == input+output.
				if u.TotalTokens == 0 {
					t.Errorf("Usage.TotalTokens zero: %+v", u)
				}
			}
			if me.Message.Provider == "" || me.Message.Model == "" || me.Message.API == "" {
				t.Errorf("assistant message missing provider/model/api: %+v", me.Message)
			}
			if me.Message.StopReason == "" {
				t.Errorf("assistant message missing stopReason")
			}
			if u.Cost.Total <= 0 {
				t.Errorf("Usage.Cost.Total = %v, want > 0", u.Cost.Total)
			}
		case stream.RoleUser:
			sawUser = true
			if me.Message.Usage != nil {
				t.Errorf("user message_end should not carry Usage: %+v", me.Message)
			}
		}
	}
	if !sawAssistant || !sawUser {
		t.Errorf("expected both assistant and user message_end (assistant=%v user=%v)", sawAssistant, sawUser)
	}
}

// TestReader_ToolExecutionEvents asserts the tool channel decodes with
// raw Args/Result so tool-specific shapes stay a render concern, and
// that start/end pair on ToolCallID.
func TestReader_ToolExecutionEvents(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture  string
		toolName string
	}{
		{"tool-read.jsonl", "read"},
		{"tool-edit.jsonl", "edit"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			r := stream.NewReader(bytes.NewReader(readFixture(t, tc.fixture)))
			events, _ := drain(t, r)

			var start *stream.ToolExecutionStart
			var end *stream.ToolExecutionEnd
			for _, ev := range events {
				switch e := ev.(type) {
				case stream.ToolExecutionStart:
					start = &e
				case stream.ToolExecutionEnd:
					end = &e
				}
			}
			if start == nil || end == nil {
				t.Fatalf("missing tool exec events (start=%v end=%v)", start != nil, end != nil)
			}
			if start.ToolName != tc.toolName || end.ToolName != tc.toolName {
				t.Errorf("toolName = %q/%q, want %q", start.ToolName, end.ToolName, tc.toolName)
			}
			if start.ToolCallID == "" || start.ToolCallID != end.ToolCallID {
				t.Errorf("ToolCallID mismatch: %q vs %q", start.ToolCallID, end.ToolCallID)
			}
			if !json.Valid(start.Args) {
				t.Errorf("Args is not valid JSON: %s", start.Args)
			}
			if !json.Valid(end.Result) {
				t.Errorf("Result is not valid JSON: %s", end.Result)
			}
			if end.IsError {
				t.Errorf("IsError = true, want false for a successful %s", tc.toolName)
			}
			// Args must carry the expected primary key, decoded by the
			// caller (render owns tool-shaped decoding; we just prove
			// the raw bytes are usable).
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(start.Args, &args); err != nil || args.Path == "" {
				t.Errorf("decode Args.path: err=%v path=%q", err, args.Path)
			}
		})
	}
}

// TestReader_UnknownTypeReturnsCarrier asserts that a bogus "type"
// yields an UnknownEvent value alongside an error matching
// ErrUnknownType, and that subsequent lines still decode.
func TestReader_UnknownTypeReturnsCarrier(t *testing.T) {
	t.Parallel()
	const input = `{"type":"totally_new_pi_thing","extra":1}
{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"/"}
`
	r := stream.NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if !errors.Is(err, stream.ErrUnknownType) {
		t.Fatalf("err = %v, want errors.Is ErrUnknownType", err)
	}
	u, ok := ev.(stream.UnknownEvent)
	if !ok {
		t.Fatalf("event = %T, want UnknownEvent", ev)
	}
	if u.Type != "totally_new_pi_thing" {
		t.Errorf("UnknownEvent.Type = %q", u.Type)
	}
	if u.Kind() != "totally_new_pi_thing" {
		t.Errorf("Kind() = %q", u.Kind())
	}
	if !json.Valid(u.Payload) {
		t.Errorf("Payload invalid JSON: %s", u.Payload)
	}
	var de *stream.DecodeError
	if !errors.As(err, &de) || de.Line != 1 {
		t.Errorf("DecodeError missing or wrong line: %+v", de)
	}

	ev, err = r.Next()
	if err != nil {
		t.Fatalf("next event after unknown: %v", err)
	}
	if _, ok := ev.(stream.Session); !ok {
		t.Errorf("expected Session after unknown, got %T", ev)
	}
}

// TestReader_KnownButUnusedIsCarrierNoError asserts that pi's
// known-but-unused event types decode to a KnownEvent carrier WITHOUT a
// paired error, so consumers tally them like any other event.
func TestReader_KnownButUnusedIsCarrierNoError(t *testing.T) {
	t.Parallel()
	const input = `{"type":"agent_start"}
{"type":"message_update","message":{},"assistantMessageEvent":{}}
{"type":"queue_update","queue":[]}
`
	r := stream.NewReader(strings.NewReader(input))
	for _, want := range []string{"agent_start", "message_update", "queue_update"} {
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("%s: unexpected error %v", want, err)
		}
		ke, ok := ev.(stream.KnownEvent)
		if !ok {
			t.Fatalf("%s: event = %T, want KnownEvent", want, ev)
		}
		if ke.Kind() != want {
			t.Errorf("Kind() = %q, want %q", ke.Kind(), want)
		}
		if !json.Valid(ke.Payload) {
			t.Errorf("%s: payload invalid JSON: %s", want, ke.Payload)
		}
	}
}

// TestReader_MalformedJSONIsRecoverable asserts a junk line produces a
// wrapped ErrMalformed with the raw bytes attached and a nil event, and
// that decoding resumes on the next line.
func TestReader_MalformedJSONIsRecoverable(t *testing.T) {
	t.Parallel()
	const input = "not-json\n" +
		`{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"/"}` + "\n"
	r := stream.NewReader(strings.NewReader(input))

	ev, err := r.Next()
	if !errors.Is(err, stream.ErrMalformed) {
		t.Fatalf("err = %v, want errors.Is ErrMalformed", err)
	}
	if ev != nil {
		t.Errorf("event = %v, want nil on malformed", ev)
	}
	var de *stream.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T", err)
	}
	if de.Line != 1 || string(de.Bytes) != "not-json" {
		t.Errorf("DecodeError fields wrong: %+v (bytes=%q)", de, de.Bytes)
	}

	ev, err = r.Next()
	if err != nil {
		t.Fatalf("next after malformed: %v", err)
	}
	if _, ok := ev.(stream.Session); !ok {
		t.Errorf("expected Session after malformed, got %T", ev)
	}
	if r.Line() != 2 {
		t.Errorf("Line = %d, want 2", r.Line())
	}
}

// TestReader_ShapeMismatchIsRecoverable asserts that a known type whose
// payload does not match the expected shape becomes a recoverable
// ErrMalformed, and decoding continues.
func TestReader_ShapeMismatchIsRecoverable(t *testing.T) {
	t.Parallel()
	// agent_end with messages as a string instead of an array.
	const input = `{"type":"agent_end","messages":"oops"}
{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"/"}
`
	r := stream.NewReader(strings.NewReader(input))
	ev, err := r.Next()
	if !errors.Is(err, stream.ErrMalformed) {
		t.Fatalf("err = %v, want errors.Is ErrMalformed", err)
	}
	if ev != nil {
		t.Errorf("event = %v, want nil on shape mismatch", ev)
	}
	ev, err = r.Next()
	if err != nil {
		t.Fatalf("next after shape mismatch: %v", err)
	}
	if _, ok := ev.(stream.Session); !ok {
		t.Errorf("expected Session after shape mismatch, got %T", ev)
	}
}

// TestStatusFromAgentEnd_Fixtures is the Q3 contract proof: the
// real-captured fixtures must parse to the documented terminal status.
func TestStatusFromAgentEnd_Fixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		want    stream.Status
	}{
		{"done.jsonl", stream.StatusDone},
		{"continue.jsonl", stream.StatusContinue},
		{"no-sentinel.jsonl", stream.StatusContinue}, // safe default
		{"tool-read.jsonl", stream.StatusContinue},
		{"tool-edit.jsonl", stream.StatusDone},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			r := stream.NewReader(bytes.NewReader(readFixture(t, tc.fixture)))
			events, _ := drain(t, r)

			var ae *stream.AgentEnd
			for _, ev := range events {
				if a, ok := ev.(stream.AgentEnd); ok {
					ae = &a
				}
			}
			if ae == nil {
				t.Fatalf("%s: no AgentEnd decoded", tc.fixture)
			}
			if got := stream.StatusFromAgentEnd(*ae); got != tc.want {
				t.Errorf("StatusFromAgentEnd = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStatusFromAgentEnd_Synthetic exercises the parser edge cases the
// real fixtures do not cover: last-match-wins across multiple sentinel
// lines, ignoring non-text blocks, taking the LAST assistant message,
// and the safe-default when no assistant message is present at all.
func TestStatusFromAgentEnd_Synthetic(t *testing.T) {
	t.Parallel()

	t.Run("last match wins", func(t *testing.T) {
		ae := stream.AgentEnd{Messages: []stream.PiMessage{{
			Role: stream.RoleAssistant,
			Content: []stream.ContentBlock{{
				Type: stream.BlockText,
				Text: "RALPH-STATUS: CONTINUE\nthen reconsidered\nRALPH-STATUS: DONE",
			}},
		}}}
		if got := stream.StatusFromAgentEnd(ae); got != stream.StatusDone {
			t.Errorf("got %v, want StatusDone", got)
		}
	})

	t.Run("last assistant message wins", func(t *testing.T) {
		ae := stream.AgentEnd{Messages: []stream.PiMessage{
			{Role: stream.RoleAssistant, Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "RALPH-STATUS: DONE"}}},
			{Role: stream.RoleToolResult, Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "RALPH-STATUS: DONE"}}},
			{Role: stream.RoleAssistant, Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "more work\nRALPH-STATUS: CONTINUE"}}},
		}}
		if got := stream.StatusFromAgentEnd(ae); got != stream.StatusContinue {
			t.Errorf("got %v, want StatusContinue", got)
		}
	})

	t.Run("thinking blocks ignored", func(t *testing.T) {
		ae := stream.AgentEnd{Messages: []stream.PiMessage{{
			Role: stream.RoleAssistant,
			Content: []stream.ContentBlock{
				{Type: stream.BlockThinking, Thinking: "RALPH-STATUS: DONE"},
				{Type: stream.BlockText, Text: "ok\nRALPH-STATUS: CONTINUE"},
			},
		}}}
		if got := stream.StatusFromAgentEnd(ae); got != stream.StatusContinue {
			t.Errorf("got %v, want StatusContinue", got)
		}
	})

	t.Run("no assistant message defaults CONTINUE", func(t *testing.T) {
		ae := stream.AgentEnd{Messages: []stream.PiMessage{
			{Role: stream.RoleUser, Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "hi"}}},
		}}
		if got := stream.StatusFromAgentEnd(ae); got != stream.StatusContinue {
			t.Errorf("got %v, want StatusContinue", got)
		}
	})

	t.Run("empty agent_end defaults CONTINUE", func(t *testing.T) {
		if got := stream.StatusFromAgentEnd(stream.AgentEnd{}); got != stream.StatusContinue {
			t.Errorf("got %v, want StatusContinue", got)
		}
	})
}

// TestReader_TruncatedReachesEOFWithoutAgentEnd proves the
// missing-terminal contract: a real capture with the agent_end line
// removed must decode every line then hit io.EOF with NO AgentEnd ever
// returned. The stream package must not fabricate one (consumers turn
// this into an iteration error).
func TestReader_TruncatedReachesEOFWithoutAgentEnd(t *testing.T) {
	t.Parallel()
	r := stream.NewReader(bytes.NewReader(readFixture(t, "truncated.jsonl")))
	events, decodeErrs := drain(t, r)

	if decodeErrs != 0 {
		t.Errorf("unexpected decode errors in truncated stream: %d", decodeErrs)
	}
	for _, ev := range events {
		if _, ok := ev.(stream.AgentEnd); ok {
			t.Fatalf("truncated stream produced an AgentEnd; it must not")
		}
	}
	if len(events) == 0 {
		t.Fatalf("expected the truncated stream to decode events before EOF")
	}
	// The last event of the truncated capture is a turn_end (the
	// agent_end line was removed); the next Next must be a clean EOF.
	if _, ok := events[len(events)-1].(stream.TurnEnd); !ok {
		t.Errorf("last event = %T, want stream.TurnEnd", events[len(events)-1])
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("post-drain Next err = %v, want io.EOF", err)
	}
}

// TestReader_EmptyStream asserts the reader reports io.EOF on an empty
// input without spurious errors.
func TestReader_EmptyStream(t *testing.T) {
	t.Parallel()
	r := stream.NewReader(strings.NewReader(""))
	ev, err := r.Next()
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
	if ev != nil {
		t.Errorf("event = %v, want nil at EOF", ev)
	}
}

// TestStatusConstants guards against accidental drift of the sentinel
// labels and the ParseStatus mapping.
func TestStatusConstants(t *testing.T) {
	t.Parallel()
	if stream.StatusDone.String() != "DONE" {
		t.Errorf("StatusDone = %q", stream.StatusDone.String())
	}
	if stream.StatusContinue.String() != "CONTINUE" {
		t.Errorf("StatusContinue = %q", stream.StatusContinue.String())
	}
	if stream.StatusUnknown.String() != "" {
		t.Errorf("StatusUnknown should print empty, got %q", stream.StatusUnknown.String())
	}
	if got, ok := stream.ParseStatus("DONE"); !ok || got != stream.StatusDone {
		t.Errorf("ParseStatus(DONE) = (%v, %v), want (StatusDone, true)", got, ok)
	}
	if got, ok := stream.ParseStatus("CONTINUE"); !ok || got != stream.StatusContinue {
		t.Errorf("ParseStatus(CONTINUE) = (%v, %v), want (StatusContinue, true)", got, ok)
	}
	if got, ok := stream.ParseStatus("nope"); ok || got != stream.StatusUnknown {
		t.Errorf("ParseStatus(nope) = (%v, %v), want (StatusUnknown, false)", got, ok)
	}
}

// TestKindMethods asserts every concrete event reports its wire-format
// discriminator via Kind().
func TestKindMethods(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   stream.Event
		want string
	}{
		{"session", stream.Session{}, stream.TypeSession},
		{"message_end", stream.MessageEnd{}, stream.TypeMessageEnd},
		{"tool_execution_start", stream.ToolExecutionStart{}, stream.TypeToolExecutionStart},
		{"tool_execution_end", stream.ToolExecutionEnd{}, stream.TypeToolExecutionEnd},
		{"turn_end", stream.TurnEnd{}, stream.TypeTurnEnd},
		{"agent_end", stream.AgentEnd{}, stream.TypeAgentEnd},
		{"known", stream.KnownEvent{Type: "message_update"}, "message_update"},
		{"unknown", stream.UnknownEvent{Type: "weird"}, "weird"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ev.Kind(); got != tc.want {
				t.Errorf("Kind() = %q, want %q", got, tc.want)
			}
		})
	}
}
