package loop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// fixturesDir is the real captured-pi fixture tree, shared with the
// stream package. The loop tests drive the same bytes through the real
// [stream.Reader] so the Q3 control flow is exercised end-to-end.
const fixturesDir = "../stream/testdata"

// readFixture loads a captured-pi JSONL fixture by base name (without
// the .jsonl suffix).
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixturesDir, name+".jsonl"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// fakeSpawner returns one [Session] per Spawn call, each backed by the
// next entry in scripts. Spawns past the end of scripts return an error
// so a runaway loop fails fast rather than hanging. The Send hook
// records each text passed through it — pi is one-shot so production
// never calls Send, but the seam is retained so a fake can assert what
// (if anything) crossed the boundary.
type fakeSpawner struct {
	scripts [][]byte
	// closeErrs, when non-nil and aligned with scripts, controls the
	// error returned by the i-th session's Close(). A nil entry means
	// Close returns nil (clean exit).
	closeErrs []error
	// configs records the agent.Config each Spawn was handed, so tests
	// can assert the kickoff prompt and persona path were forwarded.
	configs []agent.Config

	mu       sync.Mutex
	spawnIdx int
	sent     []string
}

func (f *fakeSpawner) Spawn(_ context.Context, cfg agent.Config) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.spawnIdx
	if idx >= len(f.scripts) {
		return nil, errFakeOutOfScripts
	}
	f.spawnIdx++
	f.configs = append(f.configs, cfg)
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

func (f *fakeSpawner) configAt(i int) agent.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configs[i]
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

func TestRunWith_DrivenByDoneFixture(t *testing.T) {
	sp := &fakeSpawner{scripts: [][]byte{readFixture(t, "done")}}

	var out bytes.Buffer
	err := runWith(context.Background(), minimalValidConfig(), withDuration(5*time.Second), &out, sp)
	if err != nil {
		t.Fatalf("runWith: %v", err)
	}

	// One iteration: the done fixture's agent_end carries DONE.
	if got := sp.spawnCount(); got != 1 {
		t.Errorf("spawnCount = %d, want 1", got)
	}

	// The kickoff prompt must reach pi via agent.Config.Prompt (pi is
	// one-shot; the loop never calls Send).
	cfg0 := sp.configAt(0)
	if cfg0.Prompt != "operator prompt" {
		t.Errorf("Config.Prompt = %q, want %q", cfg0.Prompt, "operator prompt")
	}
	if got := sp.sentMessages(); len(got) != 0 {
		t.Errorf("expected no Send calls (pi is one-shot), got %v", got)
	}

	// Stats panel should report a clean exit.
	if !strings.Contains(out.String(), "done") {
		t.Errorf("expected stats panel to mention 'done', got:\n%s", out.String())
	}
}

// erroringSpawner fails every Spawn. It exercises the fatal-error path
// in drive without any stream wiring.
type erroringSpawner struct{ err error }

func (e erroringSpawner) Spawn(context.Context, agent.Config) (Session, error) {
	return nil, e.err
}

func TestRunWith_PropagatesSpawnError(t *testing.T) {
	sentinel := errors.New("spawn boom")
	err := runWith(context.Background(), minimalValidConfig(), withDuration(5*time.Second), io.Discard, erroringSpawner{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error to propagate, got %v", err)
	}
}

// TestRunWith_ContinueThenDone proves the Q3 multi-iteration control
// flow: a CONTINUE agent_end advances to a fresh spawn, the next of
// which returns DONE and stops the loop.
func TestRunWith_ContinueThenDone(t *testing.T) {
	sp := &fakeSpawner{scripts: [][]byte{
		readFixture(t, "continue"),
		readFixture(t, "done"),
	}}

	err := runWith(context.Background(), minimalValidConfig(), withDuration(5*time.Second), io.Discard, sp)
	if err != nil {
		t.Fatalf("runWith: %v", err)
	}
	if got := sp.spawnCount(); got != 2 {
		t.Errorf("spawnCount = %d, want 2", got)
	}
}

// TestRunWith_TruncatedIsIterationError proves the Q3 missing-agent_end
// path: a stream that EOFs with no agent_end is an iteration error and
// the run terminates with that error (ctx is not cancelled here).
func TestRunWith_TruncatedIsIterationError(t *testing.T) {
	sp := &fakeSpawner{scripts: [][]byte{readFixture(t, "truncated")}}

	err := runWith(context.Background(), minimalValidConfig(), withDuration(5*time.Second), io.Discard, sp)
	if err == nil {
		t.Fatal("expected an iteration error from a truncated stream, got nil")
	}
	if !errors.Is(err, errStreamEnded) {
		t.Errorf("err should wrap errStreamEnded, got %v", err)
	}
}

// TestRunWith_DecodeErrorContinues asserts that a malformed JSON line
// surfaces through the emitter (visible in the writer) without halting
// the iteration: the real done fixture that follows still drives the
// loop to a DONE.
func TestRunWith_DecodeErrorContinues(t *testing.T) {
	script := append([]byte("not valid json\n"), readFixture(t, "done")...)
	sp := &fakeSpawner{scripts: [][]byte{script}}

	var out bytes.Buffer
	err := runWith(context.Background(), minimalValidConfig(), withDuration(5*time.Second), &out, sp)
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

// TestRunWith_NonZeroExitIsAdvisory confirms the Q9 contract: a
// non-zero *agent.ExitError from Close does NOT override a committed
// DONE — the iteration outcome is event-driven, the exit code is
// advisory only, so the run still succeeds.
func TestRunWith_NonZeroExitIsAdvisory(t *testing.T) {
	exitErr := &agent.ExitError{Code: 2}
	sp := &fakeSpawner{
		scripts:   [][]byte{readFixture(t, "done")},
		closeErrs: []error{exitErr},
	}

	err := runWith(context.Background(), minimalValidConfig(), withDuration(5*time.Second), io.Discard, sp)
	if err != nil {
		t.Fatalf("non-zero exit must be advisory under Q9, got err: %v", err)
	}
}

// TestRunWith_NonExitCloseErrorSurfaces confirms that a Close failure
// that is NOT an *agent.ExitError (an I/O fault) is still surfaced, so
// real plumbing breakage is not swallowed by the advisory-exit rule.
func TestRunWith_NonExitCloseErrorSurfaces(t *testing.T) {
	ioErr := errors.New("pipe reaper exploded")
	sp := &fakeSpawner{
		scripts:   [][]byte{readFixture(t, "done")},
		closeErrs: []error{ioErr},
	}

	err := runWith(context.Background(), minimalValidConfig(), withDuration(5*time.Second), io.Discard, sp)
	if !errors.Is(err, ioErr) {
		t.Fatalf("expected the non-ExitError Close failure to surface, got %v", err)
	}
}

// fixtureExists is a guard: the four Q3 fixtures must be present in the
// shared stream testdata tree for the loop tests to be meaningful.
func TestFixturesPresent(t *testing.T) {
	for _, name := range []string{"done", "continue", "no-sentinel", "truncated"} {
		if _, err := os.Stat(filepath.Join(fixturesDir, name+".jsonl")); err != nil {
			t.Fatalf("required fixture %s missing: %v", name, err)
		}
	}
}
