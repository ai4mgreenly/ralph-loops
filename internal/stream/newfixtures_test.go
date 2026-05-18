package stream_test

import (
	"bytes"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// TestReader_ToolErrorFixture decodes the real tool-error.jsonl capture
// (a `--tools read` run told to read a non-existent file, so the read
// must fail). Per Q14(c) it asserts decoded STRUCTURE and flags, not
// volatile bytes: at least one [stream.ToolExecutionEnd] reports
// IsError == true, and the terminal sentinel parses to
// [stream.StatusContinue] (the prompt ends with RALPH-STATUS: CONTINUE
// after noting the failure).
func TestReader_ToolErrorFixture(t *testing.T) {
	t.Parallel()
	r := stream.NewReader(bytes.NewReader(readFixture(t, "tool-error.jsonl")))
	events, decodeErrs := drain(t, r)

	if decodeErrs != 0 {
		t.Errorf("unexpected decode errors in tool-error fixture: %d", decodeErrs)
	}

	var (
		sawErrEnd bool
		ae        *stream.AgentEnd
	)
	for _, ev := range events {
		switch e := ev.(type) {
		case stream.ToolExecutionEnd:
			if e.IsError {
				sawErrEnd = true
			}
		case stream.AgentEnd:
			ae = &e
		}
	}

	if !sawErrEnd {
		t.Errorf("expected a ToolExecutionEnd with IsError == true (the read of a missing file must fail)")
	}
	if ae == nil {
		t.Fatalf("no AgentEnd decoded from tool-error fixture")
	}
	if got := stream.StatusFromAgentEnd(*ae); got != stream.StatusContinue {
		t.Errorf("StatusFromAgentEnd = %v, want %v", got, stream.StatusContinue)
	}
}

// TestReader_MultiTurnFixture decodes the real multi-turn.jsonl capture
// (a `--tools read,edit` run: read a file, then edit it, across
// multiple turns). Per Q14(c) it asserts decoded STRUCTURE: at least
// two [stream.ToolExecutionStart] and at least two
// [stream.ToolExecutionEnd] events (the read then the edit), exactly
// one terminal [stream.AgentEnd], and a sentinel that parses to
// [stream.StatusDone].
func TestReader_MultiTurnFixture(t *testing.T) {
	t.Parallel()
	r := stream.NewReader(bytes.NewReader(readFixture(t, "multi-turn.jsonl")))
	events, decodeErrs := drain(t, r)

	if decodeErrs != 0 {
		t.Errorf("unexpected decode errors in multi-turn fixture: %d", decodeErrs)
	}

	var (
		toolStarts, toolEnds, agentEnds int
		ae                              *stream.AgentEnd
	)
	for _, ev := range events {
		switch e := ev.(type) {
		case stream.ToolExecutionStart:
			toolStarts++
		case stream.ToolExecutionEnd:
			toolEnds++
		case stream.AgentEnd:
			agentEnds++
			ae = &e
		}
	}

	if toolStarts < 2 {
		t.Errorf("ToolExecutionStart count = %d, want >= 2 (read then edit)", toolStarts)
	}
	if toolEnds < 2 {
		t.Errorf("ToolExecutionEnd count = %d, want >= 2 (read then edit)", toolEnds)
	}
	if agentEnds != 1 {
		t.Errorf("AgentEnd count = %d, want exactly 1 (terminal)", agentEnds)
	}
	if ae == nil {
		t.Fatalf("no AgentEnd decoded from multi-turn fixture")
	}
	if got := stream.StatusFromAgentEnd(*ae); got != stream.StatusDone {
		t.Errorf("StatusFromAgentEnd = %v, want %v", got, stream.StatusDone)
	}
}
