//go:build unix

package agent_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// ExampleNewSpawner shows the canonical one-shot Spawn → Events → Close
// lifecycle against a tiny shell-script stub stand-in for the `pi` CLI.
// The stub ignores its argv (it does not need to be real pi), reads
// nothing from stdin (pi's stdin is /dev/null), and prints a minimal
// pi-native JSONL stream terminating in `agent_end` — exactly the wire
// shape the production loop consumes — so the example is a faithful
// end-to-end smoke test of the Session contract without the real
// binary. Note Send is not called: pi is one-shot and the production
// Session's Send is a documented no-op.
func ExampleNewSpawner() {
	dir, err := os.MkdirTemp("", "ralph-agent-example-")
	if err != nil {
		fmt.Println("mkdtemp:", err)
		return
	}
	defer os.RemoveAll(dir)

	// Write a stub "pi" that emits a session event and an agent_end
	// carrying one assistant message. The Spawner doesn't care that
	// it's a shell script — the only constraint is pi's JSONL shape.
	stubPath := filepath.Join(dir, "pi")
	stub := "#!/bin/sh\n" +
		`printf '%s\n' '{"type":"session","version":3,"id":"abc","timestamp":"t","cwd":"."}'` + "\n" +
		`printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"done\nRALPH-STATUS: DONE"}]}]}'` + "\n"
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		fmt.Println("write stub:", err)
		return
	}

	// The production constructor resolves the pi binary from $PATH; the
	// stub-binary seam used by integration tests is unexported, so the
	// example demonstrates the public surface by prepending the stub's
	// directory to $PATH for the duration of the call.
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	sp := agent.NewSpawner("pi")
	sp.Stderr = io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := sp.Spawn(ctx, agent.Config{
		Prompt:           "do one iteration",
		SystemPromptFile: filepath.Join(dir, "AGENTS.md"),
		WorkDir:          dir,
	})
	if err != nil {
		fmt.Println("spawn:", err)
		return
	}

	for {
		ev, err := sess.Events().Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Decode errors are forwarded to the operator in
			// production; recoverable here, so keep scanning.
			continue
		}
		if ae, ok := ev.(stream.AgentEnd); ok {
			fmt.Println("kind=", ae.Kind(), "status=", stream.StatusFromAgentEnd(ae))
			break
		}
	}

	_ = sess.Close()
	// Output: kind= agent_end status= DONE
}
