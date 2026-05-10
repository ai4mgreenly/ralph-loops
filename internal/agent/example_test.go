//go:build unix

package agent_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// ExampleNewSpawner shows the canonical Spawn → Send → Events → Close
// lifecycle against a tiny shell-script stub stand-in for the claude
// CLI. The stub reads one envelope on stdin and prints a fixed DONE
// result line; that is exactly the wire shape the production loop
// expects, so the example is a faithful end-to-end smoke test of the
// Session contract without requiring the real binary.
func ExampleNewSpawner() {
	dir, err := os.MkdirTemp("", "ralph-agent-example-")
	if err != nil {
		fmt.Println("mkdtemp:", err)
		return
	}
	defer os.RemoveAll(dir)

	// Write a stub "claude" that satisfies the wire contract: it
	// reads one user-message envelope from stdin and emits a DONE
	// result line on stdout. The agent's Spawner doesn't care that
	// it's a shell script — the only constraint is the stream-json
	// shape.
	stubPath := filepath.Join(dir, "claude")
	stub := "#!/bin/sh\n" +
		"read line\n" +
		`printf '%s\n' '{"type":"result","is_error":false,"structured_output":{"status":"DONE"}}'` + "\n"
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		fmt.Println("write stub:", err)
		return
	}

	// The production constructor resolves the engine binary from
	// $PATH; the stub-binary seam used by integration tests is
	// unexported, so the example demonstrates the public surface by
	// prepending the stub's directory to $PATH for the duration of
	// the call.
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	sp := agent.NewSpawner("claude")
	sp.Stderr = io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := sp.Spawn(ctx, agent.Config{Model: "opus", Effort: "medium", WorkDir: dir})
	if err != nil {
		fmt.Println("spawn:", err)
		return
	}
	if err := sess.Send("hello"); err != nil {
		fmt.Println("send:", err)
		_ = sess.Close()
		return
	}

	for {
		ev, err := sess.Events().Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Decode errors are forwarded to the operator in
			// production; ignore them in the example.
			if strings.Contains(err.Error(), "unknown") {
				continue
			}
			break
		}
		if r, ok := ev.(stream.Result); ok {
			fmt.Println("kind=", r.Kind(), "is_error=", r.IsError)
			break
		}
	}

	_ = sess.Close()
	// Output: kind= result is_error= false
}
