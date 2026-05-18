package loop_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/loop"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// ExampleRun_withFakeSpawner drives a complete [loop.Run] against a
// hand-rolled fake spawner feeding the real captured-pi `done` fixture
// (its terminal agent_end carries RALPH-STATUS: DONE). This is the
// smallest end-to-end shape the package supports: no subprocess, no
// real pi binary, no on-disk results log.
//
// Run writes its banner and stats panel to os.Stdout, so the example
// briefly redirects stdout to a pipe to keep the // Output: block
// stable across themes and terminal widths.
func ExampleRun_withFakeSpawner() {
	cfg := loop.Config{
		ReqsDir: "reqs",
		WorkDir: ".",
		Prompt:  "operator prompt",
		Theme:   ui.NewThemeWith(false, 0),
	}

	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := loop.Run(context.Background(), cfg,
		loop.WithSpawner(exampleSpawner{}),
		loop.WithResultsHome(""), // no JSONL side-effect
	)

	_ = w.Close()
	os.Stdout = origStdout
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()

	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Println("ok")
	// Output: ok
}

// exampleSpawner is a one-shot Spawner that yields a Session replaying
// the real captured-pi `done` fixture. The interface alignment with
// [loop.Spawner] is what makes the example portable across test files.
type exampleSpawner struct{}

func (exampleSpawner) Spawn(_ context.Context, _ agent.Config) (loop.Session, error) {
	b, err := os.ReadFile(filepath.Join("..", "stream", "testdata", "done.jsonl"))
	if err != nil {
		return nil, err
	}
	return &exampleSession{r: stream.NewReader(bytes.NewReader(b))}, nil
}

type exampleSession struct{ r *stream.Reader }

func (s *exampleSession) Events() *stream.Reader { return s.r }
func (*exampleSession) Send(string) error        { return nil }
func (*exampleSession) Close() error             { return nil }
