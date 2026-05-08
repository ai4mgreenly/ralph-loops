package loop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// fakeSpawner returns one [Session] per Spawn call, each backed by
// the next entry in scripts. Spawns past the end of scripts return an
// error so a runaway loop fails fast rather than hanging. The Send
// hook records each user message — kickoff prompt on the first
// iteration, correction text on retries — so tests can assert on the
// exact text that crossed the boundary.
type fakeSpawner struct {
	scripts [][]byte
	// closeErrs, when non-nil and aligned with scripts, controls the
	// error returned by the i-th session's Close(). A nil entry means
	// Close returns nil (clean exit).
	closeErrs []error

	mu       sync.Mutex
	spawnIdx int
	sent     []string
}

func (f *fakeSpawner) Spawn(_ context.Context, _ agent.Config) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.spawnIdx
	if idx >= len(f.scripts) {
		return nil, errFakeOutOfScripts
	}
	f.spawnIdx++
	var closeErr error
	if idx < len(f.closeErrs) {
		closeErr = f.closeErrs[idx]
	}
	return &fakeSession{
		spawner:  f,
		reader:   stream.NewReader(bytes.NewReader(f.scripts[idx])),
		closeErr: closeErr,
	}, nil
}

func (f *fakeSpawner) sentMessages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeSpawner) spawnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawnIdx
}

var errFakeOutOfScripts = errors.New("fake spawner: ran out of scripts")

type fakeSession struct {
	spawner  *fakeSpawner
	reader   *stream.Reader
	closeErr error
	closed   bool
}

func (s *fakeSession) Events() *stream.Reader { return s.reader }

func (s *fakeSession) Send(text string) error {
	s.spawner.mu.Lock()
	defer s.spawner.mu.Unlock()
	s.spawner.sent = append(s.spawner.sent, text)
	return nil
}

func (s *fakeSession) Close() error {
	s.closed = true
	return s.closeErr
}

// canned stream-json that ends with a DONE result. The system event
// in front mirrors the real claude flow.
const doneScript = `{"type":"system","subtype":"init","model":"sonnet"}
{"type":"result","is_error":false,"structured_output":{"status":"DONE"}}
`

// continueScript ends in CONTINUE so the driver loops to the next
// spawn. Useful when stitching multi-iteration tests.
const continueScript = `{"type":"result","is_error":false,"structured_output":{"status":"CONTINUE"}}
`

// badStructuredOutputScript has a result event whose structured output
// fails the DONE/CONTINUE schema check, so the iteration loops back
// for a correction.
const badStructuredOutputScript = `{"type":"result","is_error":false,"structured_output":{"status":"BOGUS"}}
`

func TestRunWith_DrivenByFakeSpawner(t *testing.T) {
	sp := &fakeSpawner{scripts: [][]byte{[]byte(doneScript)}}

	var out bytes.Buffer
	err := runWith(minimalValidConfig(), 5*time.Second, &out, sp)
	if err != nil {
		t.Fatalf("runWith: %v", err)
	}

	// Kickoff prompt must reach the agent on the first iteration.
	sent := sp.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d: %v", len(sent), sent)
	}
	if sent[0] != "operator prompt" {
		t.Errorf("kickoff prompt mismatch: %q", sent[0])
	}

	// Stats panel should report a clean exit.
	if !strings.Contains(out.String(), "done") {
		t.Errorf("expected stats panel to mention 'done', got:\n%s", out.String())
	}
}

// erroringSpawner fails every Spawn. It exercises the fatal-error
// path in drive without any stream wiring.
type erroringSpawner struct{ err error }

func (e erroringSpawner) Spawn(context.Context, agent.Config) (Session, error) {
	return nil, e.err
}

func TestRunWith_PropagatesSpawnError(t *testing.T) {
	sentinel := errors.New("spawn boom")
	err := runWith(minimalValidConfig(), 5*time.Second, io.Discard, erroringSpawner{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error to propagate, got %v", err)
	}
}

// TestRunWith_CorrectionLoopRecoversAfterBadOutput exercises the
// retry path in pumpStream: a bad structured_output triggers a
// correction message, the next attempt returns CONTINUE so the loop
// advances to a new iteration, and the third spawn returns DONE.
func TestRunWith_CorrectionLoopRecoversAfterBadOutput(t *testing.T) {
	// Scripts:
	//   1. Bad structured output → correction sent on same session.
	//      Reader then EOFs (errStreamEnded) — but the retry loop
	//      catches the correction first and re-reads. We need the
	//      bad output followed by a recoverable next read on the
	//      same session, so we glue a CONTINUE result onto the same
	//      script.
	//   2. The driver advances to a new iteration.
	//   3. DONE.
	const script1 = badStructuredOutputScript + continueScript
	sp := &fakeSpawner{scripts: [][]byte{
		[]byte(script1),
		[]byte(doneScript),
	}}

	err := runWith(minimalValidConfig(), 5*time.Second, io.Discard, sp)
	if err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if got := sp.spawnCount(); got != 2 {
		t.Errorf("spawnCount = %d, want 2", got)
	}

	sent := sp.sentMessages()
	if len(sent) < 3 {
		t.Fatalf("expected >= 3 messages (kickoff, correction, kickoff), got %d: %v", len(sent), sent)
	}
	if sent[0] != "operator prompt" {
		t.Errorf("first sent must be kickoff, got %q", sent[0])
	}
	// The second message is the correction text.
	if !strings.Contains(sent[1], "structured output") {
		t.Errorf("second message should be correction, got %q", sent[1])
	}
	if sent[2] != "operator prompt" {
		t.Errorf("third sent should be next-iteration kickoff, got %q", sent[2])
	}
}

// TestRunWith_RetryCapExhausts confirms that after maxRetriesPerIteration
// consecutive bad outputs the iteration aborts with the wrapped retry
// error and the run terminates with that error.
func TestRunWith_RetryCapExhausts(t *testing.T) {
	// One script with cap+1 bad results back-to-back. The retry loop
	// reads each bad result, sends a correction, and on the
	// (cap+1)-th attempt returns the wrapped error.
	bad := strings.Repeat(badStructuredOutputScript, maxRetriesPerIteration+2)
	sp := &fakeSpawner{scripts: [][]byte{[]byte(bad)}}

	err := runWith(minimalValidConfig(), 5*time.Second, io.Discard, sp)
	if err == nil {
		t.Fatal("expected error from retry-cap exhaustion, got nil")
	}
	if !errors.Is(err, errBadStructuredOutput) {
		t.Errorf("err should wrap errBadStructuredOutput, got %v", err)
	}
}

// TestRunWith_DecodeErrorContinues asserts that a malformed JSON
// line surfaces through the emitter (visible in the writer) without
// halting the iteration: the next event continues to be processed.
func TestRunWith_DecodeErrorContinues(t *testing.T) {
	// Malformed line, then a DONE result. The reader's
	// errBadStructuredOutput / decode-error paths must be transparent.
	script := "not valid json\n" + doneScript
	sp := &fakeSpawner{scripts: [][]byte{[]byte(script)}}

	var out bytes.Buffer
	err := runWith(minimalValidConfig(), 5*time.Second, &out, sp)
	if err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if !strings.Contains(out.String(), "decode error") {
		t.Errorf("expected decode-error notice in output, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "done") {
		t.Errorf("expected stats panel to still mention 'done', got:\n%s", out.String())
	}
}

// TestRunWith_NonZeroExitWrapsExitError confirms the loop surfaces a
// >1 exit code from the agent process as a wrapped *agent.ExitError.
func TestRunWith_NonZeroExitWrapsExitError(t *testing.T) {
	exitErr := &agent.ExitError{Code: 2}
	sp := &fakeSpawner{
		scripts:   [][]byte{[]byte(doneScript)},
		closeErrs: []error{exitErr},
	}

	err := runWith(minimalValidConfig(), 5*time.Second, io.Discard, sp)
	if err == nil {
		t.Fatal("expected non-nil error from non-zero exit, got nil")
	}
	var ee *agent.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want errors.As *agent.ExitError", err)
	}
	if ee.Code != 2 {
		t.Errorf("ExitError.Code = %d, want 2", ee.Code)
	}
}
