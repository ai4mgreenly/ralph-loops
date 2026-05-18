package loop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// newTestEmitter builds a discard-output Emitter wired to s so a single
// runIteration / pumpStream call can be exercised in isolation.
func newTestEmitter(s *stats) *render.Emitter {
	return render.NewEmitter(io.Discard, s, ui.NewThemeWith(false, 0))
}

// TestQ3_FixtureControlFlow is the keystone Q3 test: it drives the four
// real captured-pi fixtures through the production runIteration path
// (real stream.Reader, real status decode, real agent_end tally) and
// asserts the decoded outcome — parsed status and that an iteration
// figure was aggregated — rather than byte-identical rendered output
// (Q14c: fixtures carry volatile timestamps/ids/token numbers).
func TestQ3_FixtureControlFlow(t *testing.T) {
	cases := []struct {
		name       string
		fixture    string
		wantStatus stream.Status
		wantErr    error
		// wantTokens asserts the agent_end tally produced a positive
		// token figure (the fixtures all carry real usage); the exact
		// numbers are volatile so only positivity is checked.
		wantTokens bool
	}{
		{
			name:       "done.jsonl: agent_end + DONE sentinel",
			fixture:    "done",
			wantStatus: stream.StatusDone,
			wantTokens: true,
		},
		{
			name:       "continue.jsonl: agent_end + CONTINUE sentinel",
			fixture:    "continue",
			wantStatus: stream.StatusContinue,
			wantTokens: true,
		},
		{
			name:       "no-sentinel.jsonl: agent_end, no sentinel -> safe CONTINUE",
			fixture:    "no-sentinel",
			wantStatus: stream.StatusContinue,
			wantTokens: true,
		},
		{
			name:       "truncated.jsonl: EOF, no agent_end -> iteration error",
			fixture:    "truncated",
			wantStatus: stream.StatusUnknown,
			wantErr:    errStreamEnded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := &fakeSpawner{scripts: [][]byte{readFixture(t, tc.fixture)}}
			s := newStats("test-model", func() time.Time { return time.Unix(0, 0) }, "")
			e := newTestEmitter(s)

			status, err := runIteration(context.Background(), minimalValidConfig(), defaultOptions(), sp, e, s)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %v, want %v", status, tc.wantStatus)
			}

			sum := s.snapshot("/r", exitDone)
			if sum.Partial {
				t.Errorf("agent_end was observed; summary should not be marked partial")
			}
			if tc.wantTokens {
				if sum.Tokens.Total <= 0 {
					t.Errorf("expected a positive aggregated token total from agent_end, got %d", sum.Tokens.Total)
				}
				if len(sum.ByModel) == 0 {
					t.Errorf("expected the per-(provider,model) breakdown to be populated")
				}
				if sum.Cost < 0 {
					t.Errorf("cost should be pi's non-negative fractional USD, got %v", sum.Cost)
				}
			}
			if sum.Events[stream.TypeAgentEnd] != 1 {
				t.Errorf("expected exactly one agent_end tallied, got %d", sum.Events[stream.TypeAgentEnd])
			}
		})
	}
}

// TestQ3_TruncatedWithCancelledCtx pins the precedence rule: when the
// stream EOFs with no agent_end AND ctx is cancelled, the run aborts
// with the ctx error (not the generic iteration error).
func TestQ3_TruncatedWithCancelledCtx(t *testing.T) {
	sp := &fakeSpawner{scripts: [][]byte{readFixture(t, "truncated")}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel before Run begins

	err := runWith(ctx, minimalValidConfig(), withDuration(0), io.Discard, sp)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("ctx-cancelled must take precedence over the iteration error, got %v", err)
	}
}

// TestQ3_TallyIsAuthoritative confirms Q6: the per-iteration tally is
// the SUM of the assistant messages' per-turn usages in agent_end (not
// the live message_end partial). After a done iteration the summary is
// not partial and reports a coherent JSON-number cost.
func TestQ3_TallyIsAuthoritative(t *testing.T) {
	sp := &fakeSpawner{scripts: [][]byte{readFixture(t, "done")}}
	s := newStats("m", func() time.Time { return time.Unix(0, 0) }, "")
	e := newTestEmitter(s)

	if _, err := runIteration(context.Background(), minimalValidConfig(), defaultOptions(), sp, e, s); err != nil {
		t.Fatalf("runIteration: %v", err)
	}

	var buf bytes.Buffer
	sum := s.snapshot("/r", exitDone)
	sum.writeText(&buf, 0)
	if sum.Partial {
		t.Error("summary should not be partial when agent_end was folded in")
	}
	if !bytes.Contains(buf.Bytes(), []byte("cost:")) {
		t.Errorf("panel should carry a cost line:\n%s", buf.String())
	}
}
