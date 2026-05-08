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

// fakeSpawner returns a fake [agent.Session] backed by canned bytes.
// Every Spawn yields the same script; the [Send] hook lets tests
// assert the kickoff prompt that crossed the boundary.
type fakeSpawner struct {
	script []byte
	sent   []string
	mu     sync.Mutex
}

func (f *fakeSpawner) Spawn(_ context.Context, _ agent.Config) (agent.Session, error) {
	return &fakeSession{
		spawner: f,
		reader:  stream.NewReader(bytes.NewReader(f.script)),
	}, nil
}

type fakeSession struct {
	spawner *fakeSpawner
	reader  *stream.Reader
	closed  bool
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
	return nil
}

// canned stream-json that ends with a DONE result. The system event
// in front mirrors the real claude flow.
const doneScript = `{"type":"system","subtype":"init","model":"sonnet"}
{"type":"result","is_error":false,"structured_output":{"status":"DONE"}}
`

func TestRunWith_DrivenByFakeSpawner(t *testing.T) {
	sp := &fakeSpawner{script: []byte(doneScript)}

	var out bytes.Buffer
	err := runWith(minimalValidConfig(), 5*time.Second, &out, sp)
	if err != nil {
		t.Fatalf("runWith: %v", err)
	}

	// Kickoff prompt must reach the agent on the first iteration.
	if len(sp.sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d: %v", len(sp.sent), sp.sent)
	}
	if sp.sent[0] != "operator prompt" {
		t.Errorf("kickoff prompt mismatch: %q", sp.sent[0])
	}

	// Stats panel should report a clean exit.
	if !strings.Contains(out.String(), "done") {
		t.Errorf("expected stats panel to mention 'done', got:\n%s", out.String())
	}
}

// erroringSpawner fails every Spawn. It exercises the fatal-error
// path in drive without any stream wiring.
type erroringSpawner struct{ err error }

func (e erroringSpawner) Spawn(context.Context, agent.Config) (agent.Session, error) {
	return nil, e.err
}

func TestRunWith_PropagatesSpawnError(t *testing.T) {
	sentinel := errors.New("spawn boom")
	err := runWith(minimalValidConfig(), 5*time.Second, io.Discard, erroringSpawner{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error to propagate, got %v", err)
	}
}
