package stream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRawEventDecodesDiscriminator(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantType    string
		wantSubtype string
	}{
		{"assistant", `{"type":"assistant","message":{"role":"assistant","content":[]}}`, "assistant", ""},
		{"user", `{"type":"user","message":{"role":"user","content":[]}}`, "user", ""},
		{"result-success", `{"type":"result","subtype":"success","num_turns":1}`, "result", "success"},
		{"system-init", `{"type":"system","subtype":"init","model":"opus"}`, "system", "init"},
		{"rate-limit", `{"type":"rate_limit_event","rate_limit_info":{"status":"ok"}}`, "rate_limit_event", ""},
		{"unknown-type", `{"type":"weird","subtype":"x"}`, "weird", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev RawEvent
			if err := json.Unmarshal([]byte(tc.input), &ev); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if ev.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", ev.Type, tc.wantType)
			}
			if ev.Subtype != tc.wantSubtype {
				t.Errorf("Subtype = %q, want %q", ev.Subtype, tc.wantSubtype)
			}
			if string(ev.Payload) != tc.input {
				t.Errorf("Payload not preserved\n got: %s\nwant: %s", ev.Payload, tc.input)
			}
		})
	}
}

func TestRawEventRejectsBadJSON(t *testing.T) {
	var ev RawEvent
	if err := json.Unmarshal([]byte("not-json"), &ev); err == nil {
		t.Fatal("expected error decoding malformed JSON")
	}
}

func TestRawEventReusesPayloadBuffer(t *testing.T) {
	// Decoding into a non-zero RawEvent should not concatenate payloads.
	var ev RawEvent
	first := `{"type":"a","subtype":"b"}`
	second := `{"type":"c","subtype":"d"}`
	if err := json.Unmarshal([]byte(first), &ev); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(second), &ev); err != nil {
		t.Fatal(err)
	}
	if string(ev.Payload) != second {
		t.Errorf("Payload = %s, want %s", ev.Payload, second)
	}
}

func TestTwoPassDecodeAssistant(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"hi"},` +
		`{"type":"thinking","thinking":"hmm"},` +
		`{"type":"tool_use","id":"t1","name":"Read","input":{"path":"x"}}` +
		`]}}`
	var raw RawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Type != TypeAssistant {
		t.Fatalf("Type = %q, want %q", raw.Type, TypeAssistant)
	}
	var a Assistant
	if err := json.Unmarshal(raw.Payload, &a); err != nil {
		t.Fatalf("decode assistant: %v", err)
	}
	if a.Message.Role != "assistant" {
		t.Errorf("Role = %q", a.Message.Role)
	}
	if len(a.Message.Content) != 3 {
		t.Fatalf("Content len = %d, want 3", len(a.Message.Content))
	}
	if a.Message.Content[0].Type != BlockText || a.Message.Content[0].Text != "hi" {
		t.Errorf("text block = %+v", a.Message.Content[0])
	}
	if a.Message.Content[1].Type != BlockThinking || a.Message.Content[1].Thinking != "hmm" {
		t.Errorf("thinking block = %+v", a.Message.Content[1])
	}
	if a.Message.Content[2].Type != BlockToolUse || a.Message.Content[2].Name != "Read" || a.Message.Content[2].ID != "t1" {
		t.Errorf("tool_use block = %+v", a.Message.Content[2])
	}
	if !json.Valid(a.Message.Content[2].Input) {
		t.Errorf("tool_use Input is not valid JSON: %s", a.Message.Content[2].Input)
	}
}

func TestTwoPassDecodeResult(t *testing.T) {
	line := `{"type":"result","subtype":"success","num_turns":7,"duration_ms":1234,` +
		`"total_cost_usd":0.42,"is_error":false,` +
		`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3},` +
		`"structured_output":{"status":"DONE"}}`
	var raw RawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(raw.Payload, &r); err != nil {
		t.Fatal(err)
	}
	if r.NumTurns != 7 || r.DurationMS != 1234 || r.TotalCostUSD != 0.42 || r.IsError {
		t.Errorf("scalar fields wrong: %+v", r)
	}
	if r.Usage == nil || r.Usage.InputTokens != 10 || r.Usage.OutputTokens != 20 ||
		r.Usage.CacheReadInputTokens != 5 || r.Usage.CacheCreationInputTokens != 3 {
		t.Errorf("usage wrong: %+v", r.Usage)
	}
	var so StatusOutput
	if err := json.Unmarshal(r.StructuredOutput, &so); err != nil {
		t.Fatalf("decode structured_output: %v", err)
	}
	if so.Status != StatusDone {
		t.Errorf("Status = %q, want %q", so.Status, StatusDone)
	}
}

func TestTwoPassDecodeUserToolResult(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"t1","is_error":false,"content":"ok"}` +
		`]},"tool_use_result":{"file":"x"}}`
	var raw RawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatal(err)
	}
	var u User
	if err := json.Unmarshal(raw.Payload, &u); err != nil {
		t.Fatal(err)
	}
	if len(u.Message.Content) != 1 {
		t.Fatalf("Content len = %d", len(u.Message.Content))
	}
	b := u.Message.Content[0]
	if b.Type != BlockToolResult || b.ToolUseID != "t1" || b.IsError {
		t.Errorf("tool_result block wrong: %+v", b)
	}
	if !json.Valid(u.ToolUseResult) {
		t.Errorf("ToolUseResult invalid JSON: %s", u.ToolUseResult)
	}
}

func TestTwoPassDecodeSystem(t *testing.T) {
	line := `{"type":"system","subtype":"init","model":"opus","cwd":"/tmp",` +
		`"permissionMode":"bypass","tools":["Read","Edit"]}`
	var raw RawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatal(err)
	}
	var s System
	if err := json.Unmarshal(raw.Payload, &s); err != nil {
		t.Fatal(err)
	}
	if s.Subtype != "init" || s.Model != "opus" || s.Cwd != "/tmp" ||
		s.PermissionMode != "bypass" {
		t.Errorf("system fields wrong: %+v", s)
	}
	if len(s.Tools) != 2 || s.Tools[0] != "Read" || s.Tools[1] != "Edit" {
		t.Errorf("Tools = %v", s.Tools)
	}
}

func TestTwoPassDecodeRateLimit(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{` +
		`"rateLimitType":"tokens","status":"warning","utilization":0.75,` +
		`"resetsAt":1700000000,"isUsingOverage":true}}`
	var raw RawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatal(err)
	}
	var rl RateLimit
	if err := json.Unmarshal(raw.Payload, &rl); err != nil {
		t.Fatal(err)
	}
	if rl.Info == nil {
		t.Fatal("Info is nil")
	}
	if rl.Info.RateLimitType != "tokens" || rl.Info.Status != "warning" ||
		rl.Info.Utilization != 0.75 || rl.Info.ResetsAt != 1700000000 ||
		!rl.Info.IsUsingOverage {
		t.Errorf("rate-limit fields wrong: %+v", rl.Info)
	}
}

func TestSchemaJSONIsValid(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(SchemaJSON), &v); err != nil {
		t.Fatalf("SchemaJSON does not parse: %v", err)
	}
	// Sanity: the schema should mention both status values it constrains.
	if !strings.Contains(SchemaJSON, StatusDone) || !strings.Contains(SchemaJSON, StatusContinue) {
		t.Errorf("SchemaJSON should constrain status to %q and %q", StatusDone, StatusContinue)
	}
}

func TestStatusConstants(t *testing.T) {
	// Guard against accidental drift — these strings are part of the
	// wire contract with claude --json-schema.
	if StatusDone != "DONE" {
		t.Errorf("StatusDone = %q", StatusDone)
	}
	if StatusContinue != "CONTINUE" {
		t.Errorf("StatusContinue = %q", StatusContinue)
	}
}
