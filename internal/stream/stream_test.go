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

// TestReader_DispatchesAllKnownTypes feeds one of every known event
// kind through Reader.Next and asserts the concrete type plus a few
// representative payload fields on each. The input is a realistic
// session loaded from testdata/session.jsonl so the fixture doubles
// as documentation of the wire format.
func TestReader_DispatchesAllKnownTypes(t *testing.T) {
	t.Parallel()

	input, err := os.ReadFile(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	r := stream.NewReader(bytes.NewReader(input))

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
	if err := json.Unmarshal(res.StructuredOutput, &so); err != nil || so.Status != stream.StatusDone.String() {
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
	if so.Status != stream.StatusContinue.String() {
		t.Errorf("Status = %q, want %q", so.Status, stream.StatusContinue.String())
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
	if !strings.Contains(stream.SchemaJSON, stream.StatusDone.String()) ||
		!strings.Contains(stream.SchemaJSON, stream.StatusContinue.String()) {
		t.Errorf("SchemaJSON should constrain status to %q and %q",
			stream.StatusDone.String(), stream.StatusContinue.String())
	}
}

// TestWriteUserMessage_Framing exercises the user-message envelope used
// to feed claude on stdin: the framing must end with a newline and the
// envelope must round-trip through json.Unmarshal with the original
// text intact.
func TestWriteUserMessage_Framing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := stream.WriteUserMessage(&buf, "hello"); err != nil {
		t.Fatalf("WriteUserMessage: %v", err)
	}
	out := buf.Bytes()
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Fatalf("user message must end with newline, got %q", out)
	}

	// Round-trip through a generic shape (the envelope types are
	// unexported in stream; an any-typed decode is enough to assert
	// the on-the-wire structure).
	var decoded struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &decoded); err != nil {
		t.Fatalf("unmarshal written line: %v", err)
	}
	if decoded.Type != "user" || decoded.Message.Role != "user" {
		t.Errorf("envelope roles wrong: %+v", decoded)
	}
	if len(decoded.Message.Content) != 1 ||
		decoded.Message.Content[0].Type != "text" ||
		decoded.Message.Content[0].Text != "hello" {
		t.Errorf("content not preserved: %+v", decoded.Message.Content)
	}
}

// TestStatusConstants guards against accidental drift of wire-protocol
// strings.
func TestStatusConstants(t *testing.T) {
	t.Parallel()
	if stream.StatusDone.String() != "DONE" {
		t.Errorf("StatusDone = %q", stream.StatusDone.String())
	}
	if stream.StatusContinue.String() != "CONTINUE" {
		t.Errorf("StatusContinue = %q", stream.StatusContinue.String())
	}
	if stream.StatusUnknown.String() != "" {
		t.Errorf("StatusUnknown should print as empty, got %q", stream.StatusUnknown.String())
	}
	if got, ok := stream.ParseStatus("DONE"); !ok || got != stream.StatusDone {
		t.Errorf("ParseStatus(DONE) = (%v, %v), want (StatusDone, true)", got, ok)
	}
	if got, ok := stream.ParseStatus("nope"); ok || got != stream.StatusUnknown {
		t.Errorf("ParseStatus(nope) = (%v, %v), want (StatusUnknown, false)", got, ok)
	}
}
