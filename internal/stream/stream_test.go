package stream_test

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// TestReader_DispatchesAllKnownTypes feeds one of every known event
// kind through Reader.Next and asserts the concrete type plus a few
// representative payload fields on each.
func TestReader_DispatchesAllKnownTypes(t *testing.T) {
	t.Parallel()

	const input = `{"type":"system","subtype":"init","model":"opus","tools":["Read","Edit"]}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"t1","name":"Read","input":{"path":"x"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":false,"content":"ok"}]},"tool_use_result":{"file":"x"}}
{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"tokens","status":"warning","utilization":0.75,"resetsAt":1700000000,"isUsingOverage":true}}
{"type":"result","subtype":"success","num_turns":7,"duration_ms":1234,"total_cost_usd":0.42,"is_error":false,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3},"structured_output":{"status":"DONE"}}
`

	r := stream.NewReader(strings.NewReader(input))

	// system
	ev, err := r.Next()
	if err != nil {
		t.Fatalf("system: %v", err)
	}
	sys, ok := ev.(stream.System)
	if !ok {
		t.Fatalf("first event = %T, want stream.System", ev)
	}
	if sys.Model != "opus" || len(sys.Tools) != 2 || sys.Tools[0] != "Read" {
		t.Errorf("system payload wrong: %+v", sys)
	}
	if r.Line() != 1 {
		t.Errorf("Line = %d, want 1", r.Line())
	}

	// assistant
	ev, err = r.Next()
	if err != nil {
		t.Fatalf("assistant: %v", err)
	}
	asst, ok := ev.(stream.Assistant)
	if !ok {
		t.Fatalf("second event = %T, want stream.Assistant", ev)
	}
	if len(asst.Message.Content) != 2 ||
		asst.Message.Content[0].Type != stream.BlockText ||
		asst.Message.Content[1].Type != stream.BlockToolUse ||
		asst.Message.Content[1].Name != "Read" {
		t.Errorf("assistant payload wrong: %+v", asst)
	}

	// user
	ev, err = r.Next()
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	usr, ok := ev.(stream.User)
	if !ok {
		t.Fatalf("third event = %T, want stream.User", ev)
	}
	if len(usr.Message.Content) != 1 || usr.Message.Content[0].ToolUseID != "t1" {
		t.Errorf("user payload wrong: %+v", usr)
	}
	if !json.Valid(usr.ToolUseResult) {
		t.Errorf("ToolUseResult invalid JSON: %s", usr.ToolUseResult)
	}

	// rate_limit
	ev, err = r.Next()
	if err != nil {
		t.Fatalf("rate_limit: %v", err)
	}
	rl, ok := ev.(stream.RateLimit)
	if !ok {
		t.Fatalf("fourth event = %T, want stream.RateLimit", ev)
	}
	if rl.Info == nil || rl.Info.RateLimitType != "tokens" || !rl.Info.IsUsingOverage {
		t.Errorf("rate_limit payload wrong: %+v", rl.Info)
	}

	// result
	ev, err = r.Next()
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	res, ok := ev.(stream.Result)
	if !ok {
		t.Fatalf("fifth event = %T, want stream.Result", ev)
	}
	if res.NumTurns != 7 || res.DurationMS != 1234 || res.Usage == nil || res.Usage.InputTokens != 10 {
		t.Errorf("result payload wrong: %+v", res)
	}
	var so stream.StatusOutput
	if err := json.Unmarshal(res.StructuredOutput, &so); err != nil || so.Status != stream.StatusDone {
		t.Errorf("structured_output decode wrong: %v %q", err, so.Status)
	}

	// EOF
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("end-of-stream err = %v, want io.EOF", err)
	}
}

// TestReader_KindMethodMatchesType asserts that every concrete event
// reports the wire-format discriminator via Kind().
func TestReader_KindMethodMatchesType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   stream.Event
		want string
	}{
		{"assistant", stream.Assistant{}, stream.TypeAssistant},
		{"user", stream.User{}, stream.TypeUser},
		{"result", stream.Result{}, stream.TypeResult},
		{"system", stream.System{}, stream.TypeSystem},
		{"rate_limit", stream.RateLimit{}, stream.TypeRateLimit},
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

// TestReader_UnknownTypeReturnsCarrier asserts that an unrecognised
// event "type" yields an UnknownEvent value alongside an error
// matching ErrUnknownType. Subsequent lines must still decode.
func TestReader_UnknownTypeReturnsCarrier(t *testing.T) {
	t.Parallel()
	const input = `{"type":"weird","subtype":"x","extra":1}
{"type":"system","model":"opus"}
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
	if u.Type != "weird" || u.Subtype != "x" {
		t.Errorf("UnknownEvent fields wrong: %+v", u)
	}
	if !json.Valid(u.Payload) {
		t.Errorf("Payload invalid JSON: %s", u.Payload)
	}
	var de *stream.DecodeError
	if !errors.As(err, &de) || de.Line != 1 {
		t.Errorf("DecodeError missing or wrong line: %+v", de)
	}

	// Reader must continue after an unknown type.
	ev, err = r.Next()
	if err != nil {
		t.Fatalf("next event after unknown: %v", err)
	}
	if _, ok := ev.(stream.System); !ok {
		t.Errorf("expected system after unknown, got %T", ev)
	}
}

// TestReader_MalformedJSONIsRecoverable asserts that a junk line
// produces a wrapped ErrMalformed with the raw bytes attached, and
// that decoding continues on the next line.
func TestReader_MalformedJSONIsRecoverable(t *testing.T) {
	t.Parallel()
	const input = "not-json\n" +
		`{"type":"system","model":"opus"}` + "\n"
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
	if _, ok := ev.(stream.System); !ok {
		t.Errorf("expected system after malformed, got %T", ev)
	}
	if r.Line() != 2 {
		t.Errorf("Line = %d, want 2", r.Line())
	}
}

// TestReader_ResultStructuredOutput rounds out coverage of the
// schema-constrained terminal event.
func TestReader_ResultStructuredOutput(t *testing.T) {
	t.Parallel()
	const input = `{"type":"result","structured_output":{"status":"CONTINUE"}}` + "\n"
	r := stream.NewReader(strings.NewReader(input))
	ev, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	res, ok := ev.(stream.Result)
	if !ok {
		t.Fatalf("event = %T, want Result", ev)
	}
	var so stream.StatusOutput
	if err := json.Unmarshal(res.StructuredOutput, &so); err != nil {
		t.Fatal(err)
	}
	if so.Status != stream.StatusContinue {
		t.Errorf("Status = %q, want %q", so.Status, stream.StatusContinue)
	}
}

// TestReader_EmptyStream asserts the reader reports io.EOF on an
// empty input without spurious errors.
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

// TestSchemaJSONIsValid guards the wire schema constant.
func TestSchemaJSONIsValid(t *testing.T) {
	t.Parallel()
	var v any
	if err := json.Unmarshal([]byte(stream.SchemaJSON), &v); err != nil {
		t.Fatalf("SchemaJSON does not parse: %v", err)
	}
	if !strings.Contains(stream.SchemaJSON, stream.StatusDone) ||
		!strings.Contains(stream.SchemaJSON, stream.StatusContinue) {
		t.Errorf("SchemaJSON should constrain status to %q and %q",
			stream.StatusDone, stream.StatusContinue)
	}
}

// TestStatusConstants guards against accidental drift of wire-protocol
// strings.
func TestStatusConstants(t *testing.T) {
	t.Parallel()
	if stream.StatusDone != "DONE" {
		t.Errorf("StatusDone = %q", stream.StatusDone)
	}
	if stream.StatusContinue != "CONTINUE" {
		t.Errorf("StatusContinue = %q", stream.StatusContinue)
	}
}
