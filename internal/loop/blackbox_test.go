package loop_test

import (
	"bytes"
	"context"
	"errors"
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

// blackboxSpawner is the minimal external implementation of the
// public [loop.Spawner] interface. Living in package loop_test is
// the point: it forces the public surface to be sufficient for an
// outside consumer to drive the loop end-to-end without reaching
// into unexported helpers.
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
// package and asserts that a DONE iteration produces a JSONL record
// whose exit field is "done".
func TestRun_Blackbox_HappyPath(t *testing.T) {
	cfg := loop.Config{
		ReqsDir: "reqs",
		WorkDir: ".",
		Prompt:  "operator prompt",
		Theme:   ui.NewThemeWith(false, 0),
	}
	tmp := t.TempDir()
	const script = `{"type":"result","is_error":false,"structured_output":{"status":"DONE"}}` + "\n"

	// Suppress Run's banner/panel; this is the public-API equivalent
	// of the package-internal runWith(w, ...) seam.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := loop.Run(context.Background(), cfg,
		loop.WithSpawner(blackboxSpawner{script: []byte(script)}),
		loop.WithResultsHome(tmp),
		loop.WithModel("opus"),
		loop.WithEffort("medium"),
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
}

// TestRun_Blackbox_RejectsBadEffort confirms the public validation
// path surfaces ErrInvalidConfig from outside the package.
func TestRun_Blackbox_RejectsBadEffort(t *testing.T) {
	cfg := loop.Config{
		ReqsDir: "reqs",
		WorkDir: ".",
		Prompt:  "operator prompt",
		Theme:   ui.NewThemeWith(false, 0),
	}
	err := loop.Run(context.Background(), cfg, loop.WithEffort("ludicrous"))
	if !errors.Is(err, loop.ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}
