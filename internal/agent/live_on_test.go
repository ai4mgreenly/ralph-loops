//go:build unix && pilive

package agent_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// runLivePiSmoke is the real Q14(b) implementation, compiled only under
// `-tags pilive`. It is still runtime-gated so even a pilive build
// stays green in CI/unauthed environments:
//
//   - RALPH_PI_LIVE must be exactly "1"; AND
//   - `pi` must resolve on $PATH.
//
// Either gate unmet ⇒ t.Skip with a clear reason. When both hold it
// spawns the real `pi` via the production [agent.Spawner], drives the
// stream to EOF, and asserts at least one [stream.AgentEnd] was seen
// and that [stream.StatusFromAgentEnd] parsed a real sentinel
// (StatusDone or StatusContinue — never the StatusUnknown zero value).
// A 120s context bounds a hung pi so it cannot wedge the suite.
func runLivePiSmoke(t *testing.T) {
	if os.Getenv("RALPH_PI_LIVE") != "1" {
		t.Skip("live pi smoke test skipped: set RALPH_PI_LIVE=1 to run it (it costs real API calls and needs pi authed)")
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("live pi smoke test skipped: `pi` not found on $PATH (CI/unauthed environments skip cleanly)")
	}

	// Trivial --no-tools-equivalent prompt: a one-word reply plus the
	// bare sentinel line. No SystemPromptFile, default tools — the
	// prompt asks for no tool use so the round-trip stays fast/cheap;
	// the point is exercising the real wire-decode + sentinel parse,
	// not the build-agent persona.
	const prompt = "Reply with the single word: working. " +
		"Then output a final line containing exactly: RALPH-STATUS: CONTINUE"

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sp := agent.NewSpawner("pi")
	sp.Stderr = io.Discard
	sess, err := sp.Spawn(ctx, agent.Config{Prompt: prompt, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn(pi): %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	var (
		sawAgentEnd bool
		status      stream.Status
	)
	for {
		ev, err := sess.Events().Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Decode errors are recoverable: the stream stays positioned
			// on the next line. In production these are surfaced to the
			// operator; here we keep scanning to reach EOF.
			continue
		}
		if ae, ok := ev.(stream.AgentEnd); ok {
			sawAgentEnd = true
			status = stream.StatusFromAgentEnd(ae)
		}
	}

	if !sawAgentEnd {
		t.Fatal("no agent_end observed from live pi: terminal event missing (possible 0.x format drift)")
	}
	switch status {
	case stream.StatusDone, stream.StatusContinue:
		// A real sentinel parsed — the end-to-end contract holds.
	default:
		t.Fatalf("StatusFromAgentEnd = %v (StatusUnknown), want StatusDone or StatusContinue: sentinel parse broke (possible 0.x format drift)", status)
	}
}
