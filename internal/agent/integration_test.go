//go:build unix

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// These tests exercise the one-shot Spawn → Events → Close lifecycle
// against a real subprocess. We use the canonical "re-exec the test
// binary" pattern: each test launches the test binary itself, a
// sentinel env var (GO_WANT_HELPER_PROCESS=1) routes execution into
// [TestHelperProcess], and another env var (HELPER_MODE) selects the
// behaviour the helper performs. This keeps the integration entirely
// in-tree — no external pi binary required. (A live pi smoke test is a
// separate, gated concern per the migration record.)

// helperSpawner returns a [*Spawner] wired to re-exec the test binary
// in helper mode. The cfg-shaped pi flags are still produced (so we
// exercise the real argv path) but the helper just ignores them.
func helperSpawner(t *testing.T, mode string) *Spawner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// "--" separates test framework flags from the production argv the
	// spawner appends. Without the separator, Go's testing package
	// would gobble the pi flags as unknown test flags.
	sp := newSpawnerWithExtraArgs(exe, "-test.run=TestHelperProcess", "--")
	sp.Stderr = io.Discard
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("HELPER_MODE", mode)
	return sp
}

// helperConfig is a minimal valid pi Config for the helper tests. The
// helper ignores argv, but a real Prompt/SystemPromptFile keeps
// buildArgs on its production path.
func helperConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Prompt:           "kickoff",
		SystemPromptFile: "/abs/AGENTS.md",
		WorkDir:          t.TempDir(),
	}
}

// TestHelperProcess is the re-exec target. When GO_WANT_HELPER_PROCESS
// is set, this branches on HELPER_MODE and emulates a tiny piece of pi
// print mode. It deliberately does NOT read stdin: production pi's
// stdin is /dev/null, and these helpers must not block on it.
//
//   - "happy"   : print a session event then an agent_end (DONE),
//     exit 0.
//   - "sigterm" : trap SIGTERM, exit 7 when received, otherwise spin.
//   - "hang"    : print one event, then sleep until killed.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	switch os.Getenv("HELPER_MODE") {
	case "happy":
		fmt.Println(`{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"."}`)
		fmt.Println(`{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"RALPH-STATUS: DONE"}]}]}`)
		os.Exit(0)
	case "sigterm":
		ch := make(chan os.Signal, 1)
		// pi print-mode handles SIGTERM (clean exit 143) and installs no
		// SIGINT handler; ralph's Cancel hook sends SIGTERM to the group.
		signal.Notify(ch, syscall.SIGTERM)
		fmt.Println(`{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"."}`)
		select {
		case <-ch:
			os.Exit(7)
		case <-time.After(30 * time.Second):
			os.Exit(0)
		}
	case "hang":
		fmt.Println(`{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"."}`)
		time.Sleep(60 * time.Second)
		os.Exit(0)
	default:
		os.Exit(0)
	}
}

// drainUntil reads events from r until it sees one whose Kind matches
// kind, or until r returns EOF / a fatal error.
func drainUntil(t *testing.T, r *stream.Reader, kind string) stream.Event {
	t.Helper()
	for {
		ev, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			// Decode errors are surfaced to the loop in production;
			// here we just keep scanning.
			continue
		}
		if ev.Kind() == kind {
			return ev
		}
	}
}

func TestSpawner_HappyPath(t *testing.T) {
	sp := helperSpawner(t, "happy")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sp.Spawn(ctx, helperConfig(t))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if ev := drainUntil(t, sess.Events(), stream.TypeAgentEnd); ev == nil {
		_ = sess.Close()
		t.Fatal("did not see agent_end event before EOF")
	}
	if err := sess.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestSpawner_SendIsNoOp documents that the production pi Session's
// Send is a no-op (one-shot mode: the prompt was already argv) and
// never errors, so loop call sites that still invoke it stay harmless.
func TestSpawner_SendIsNoOp(t *testing.T) {
	sp := helperSpawner(t, "happy")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sp.Spawn(ctx, helperConfig(t))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := sess.Send("ignored"); err != nil {
		t.Errorf("production Send must be a nil-returning no-op, got %v", err)
	}
	drainUntil(t, sess.Events(), stream.TypeAgentEnd)
	_ = sess.Close()
}

func TestSpawner_CloseIsIdempotent(t *testing.T) {
	sp := helperSpawner(t, "happy")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sp.Spawn(ctx, helperConfig(t))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	drainUntil(t, sess.Events(), stream.TypeAgentEnd)

	first := sess.Close()
	second := sess.Close()
	// Both calls return the same value because closeOnce gates the real
	// work and closeErr is never reassigned afterward.
	if first != second {
		t.Errorf("Close non-idempotent: first=%v second=%v", first, second)
	}
}

func TestSpawner_CancelDeliversSignal(t *testing.T) {
	sp := helperSpawner(t, "sigterm")
	ctx, cancel := context.WithCancel(context.Background())
	sess, err := sp.Spawn(ctx, helperConfig(t))
	if err != nil {
		cancel()
		t.Fatalf("Spawn: %v", err)
	}
	// Wait for the helper to print its first event so we know its
	// signal handler is installed before we cancel.
	if ev := drainUntil(t, sess.Events(), stream.TypeSession); ev == nil {
		cancel()
		_ = sess.Close()
		t.Fatal("did not observe session event before cancel")
	}

	cancel()
	err = sess.Close()
	if err == nil {
		t.Fatal("expected ExitError after cancel, got nil")
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExitError, got %T (%v)", err, err)
	}
	// Two acceptable shapes:
	//
	//   1. Helper trapped SIGTERM and exited 7 cleanly. Signaled=false,
	//      Code=7. This is the documented happy path (pi handles
	//      SIGTERM cleanly).
	//   2. WaitDelay's SIGKILL escalation reached the helper before it
	//      could exit on its own. Signaled=true, Signal=SIGKILL (or
	//      SIGTERM if the runtime delivered it as a signal death).
	//
	// Either way, the cancel reached the child via SIGTERM to the
	// process group — which is the contract under test.
	switch {
	case !ee.Signaled && ee.Code == 7:
		// canonical case
	case ee.Signaled && (ee.Signal == syscall.SIGTERM || ee.Signal == syscall.SIGKILL):
		// escalation case
	default:
		t.Errorf("unexpected exit shape: %+v", ee)
	}
}

func TestSpawner_StdoutTapMirrorsBytes(t *testing.T) {
	sp := helperSpawner(t, "happy")
	var tap bytes.Buffer
	sp.Stdout = &tap

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sp.Spawn(ctx, helperConfig(t))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if ev := drainUntil(t, sess.Events(), stream.TypeAgentEnd); ev == nil {
		_ = sess.Close()
		t.Fatal("did not see agent_end event before EOF")
	}
	if err := sess.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	got := tap.String()
	// The "happy" helper prints exactly two lines verbatim; the tap
	// must observe both, byte-for-byte, in order. This is the
	// engine-neutral mechanism --raw relies on.
	want := []string{
		`{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"."}` + "\n",
		`{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"RALPH-STATUS: DONE"}]}]}` + "\n",
	}
	for _, line := range want {
		if !strings.Contains(got, line) {
			t.Errorf("tap missing line %q\nfull tap: %q", line, got)
		}
	}
	if got != want[0]+want[1] {
		t.Errorf("tap content = %q, want %q", got, want[0]+want[1])
	}
}

func TestSpawner_HangingChildIsKilled(t *testing.T) {
	sp := helperSpawner(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := sp.Spawn(ctx, helperConfig(t))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cs, ok := sess.(*engineSession)
	if !ok {
		t.Fatalf("expected *engineSession, got %T", sess)
	}
	pid := cs.cmd.Process.Pid

	// Wait for the first event so the helper has reached its sleep.
	if ev := drainUntil(t, sess.Events(), stream.TypeSession); ev == nil {
		_ = sess.Close()
		t.Fatal("did not observe session event")
	}

	// Close should escalate to Kill within closeGrace; pad with
	// generous slack so a slow CI box does not flake.
	slack := 5 * time.Second
	bound := closeGrace + waitDelay + slack
	done := make(chan error, 1)
	go func() { done <- sess.Close() }()

	select {
	case err := <-done:
		if err == nil {
			t.Errorf("expected non-nil error from Close on hanging child, got nil")
		}
	case <-time.After(bound):
		t.Fatalf("Close did not return within %v", bound)
	}

	// The child must be reaped; kill(pid, 0) returns ESRCH on a gone
	// process. Wait briefly for the OS to clean up the zombie.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if err != nil && errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("kill(%d, 0) did not report ESRCH after Close", pid)
}
