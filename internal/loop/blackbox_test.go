package loop_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/loop"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// fixtureBytes loads a captured-pi JSONL fixture from the shared stream
// testdata tree. The blackbox test deliberately reaches the public API
// only; the fixture path is the one external knob it needs.
func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "stream", "testdata", name+".jsonl"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// blackboxSpawner is the minimal external implementation of the public
// [loop.Spawner] interface. Living in package loop_test is the point:
// it forces the public surface to be sufficient for an outside consumer
// to drive the loop end-to-end without reaching into unexported
// helpers.
type blackboxSpawner struct {
	script []byte
}

func (b blackboxSpawner) Spawn(_ context.Context, _ agent.Config) (loop.Session, error) {
	return &blackboxSession{r: stream.NewReader(bytes.NewReader(b.script))}, nil
}

type blackboxSession struct{ r *stream.Reader }

func (s *blackboxSession) Events() *stream.Reader { return s.r }
func (*blackboxSession) Send(string) error        { return nil }
func (*blackboxSession) Close() error             { return nil }

// TestRun_Blackbox_HappyPath runs a complete loop from outside the
// package against the real captured-pi `done` fixture and asserts that
// the DONE iteration produces a JSONL record whose exit field is
// "done".
func TestRun_Blackbox_HappyPath(t *testing.T) {
	cfg := loop.Config{
		ReqsDir: "reqs",
		WorkDir: ".",
		Prompt:  "operator prompt",
		Theme:   ui.NewThemeWith(false, 0),
	}
	tmp := t.TempDir()

	// Suppress Run's banner/panel; this is the public-API equivalent
	// of the package-internal runWith(w, ...) seam.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := loop.Run(context.Background(), cfg,
		loop.WithSpawner(blackboxSpawner{script: fixtureBytes(t, "done")}),
		loop.WithResultsHome(tmp),
		loop.WithModel("gpt-5.3-codex"),
	)
	_ = w.Close()
	os.Stdout = origStdout
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body, ferr := os.ReadFile(filepath.Join(tmp, "results.jsonl"))
	if ferr != nil {
		t.Fatalf("read jsonl: %v", ferr)
	}
	if !strings.Contains(string(body), `"exit":"done"`) {
		t.Errorf("expected exit=done in jsonl, got: %s", body)
	}
	// Cost is now pi's authoritative fractional USD; an unknown model
	// name no longer fails fast (the pricing-table gate is gone), and
	// the record still carries a coherent JSON cost number.
	if !strings.Contains(string(body), `"cost":`) {
		t.Errorf("expected a cost field in the JSONL record, got: %s", body)
	}
}

// TestRun_Blackbox_UnknownModelNoLongerRejected pins the Q6 change:
// because cost comes from pi (not a local pricing table), an arbitrary
// model string is accepted — there is no startup rejection.
func TestRun_Blackbox_UnknownModelNoLongerRejected(t *testing.T) {
	cfg := loop.Config{
		ReqsDir: "reqs",
		WorkDir: ".",
		Prompt:  "operator prompt",
		Theme:   ui.NewThemeWith(false, 0),
	}
	tmp := t.TempDir()

	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := loop.Run(context.Background(), cfg,
		loop.WithSpawner(blackboxSpawner{script: fixtureBytes(t, "done")}),
		loop.WithResultsHome(tmp),
		loop.WithModel("not-a-real-model"),
	)
	_ = w.Close()
	os.Stdout = origStdout
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()

	if err != nil {
		t.Fatalf("an arbitrary model must be accepted under pi (no pricing gate), got: %v", err)
	}
}
