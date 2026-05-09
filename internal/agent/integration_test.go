//go:build unix

package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// These tests exercise the Spawn → Send → Events → Close lifecycle
// against a real subprocess. We use the canonical "re-exec the test
// binary" pattern: each test launches the test binary itself, a
// sentinel env var (GO_WANT_HELPER_PROCESS=1) routes execution into
// [TestHelperProcess], and another env var (HELPER_MODE) selects the
// behaviour the helper performs. This keeps the integration entirely
// in-tree — no external claude binary required.

// helperSpawner returns a [*Spawner] wired to re-exec the test binary
// in helper mode. The cfg-shaped flags claude expects are still
// produced (so we exercise the real argv path) but the helper just
// ignores them.
func helperSpawner(t *testing.T, mode string) *Spawner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// "--" separates test framework flags from the production argv
	// the spawner appends. Without the separator, Go's testing
	// package would gobble the claude flags as unknown test flags.
	sp := newSpawnerWithBinary(exe, "-test.run=TestHelperProcess", "--")
	sp.Stderr = io.Discard
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("HELPER_MODE", mode)
	return sp
}

// TestHelperProcess is the re-exec target. When GO_WANT_HELPER_PROCESS
// is set, this branches on HELPER_MODE and emulates a tiny piece of
// the claude CLI:
//
//   - "happy"    : read one envelope on stdin, print system + DONE
//     result, exit 0.
//   - "sigtrap"  : trap SIGINT, exit 7 when received, otherwise spin.
//   - "hang"     : print one event, then sleep until killed.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	switch os.Getenv("HELPER_MODE") {
	case "happy":
		sc := bufio.NewScanner(os.Stdin)
		_ = sc.Scan()
		fmt.Println(`{"type":"system","subtype":"init","model":"sonnet"}`)
		fmt.Println(`{"type":"result","is_error":false,"structured_output":{"status":"DONE"}}`)
		os.Exit(0)
	case "sigtrap":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		fmt.Println(`{"type":"system","subtype":"init"}`)
		select {
		case <-ch:
			os.Exit(7)
		case <-time.After(30 * time.Second):
			os.Exit(0)
		}
	case "hang":
		fmt.Println(`{"type":"system","subtype":"init"}`)
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
	sess, err := sp.Spawn(ctx, Config{Model: "opus", Effort: "medium", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := sess.Send("kickoff"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ev := drainUntil(t, sess.Events(), stream.TypeResult); ev == nil {
		_ = sess.Close()
		t.Fatal("did not see result event before EOF")
	}
	if err := sess.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestSpawner_CloseIsIdempotent(t *testing.T) {
	sp := helperSpawner(t, "happy")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sp.Spawn(ctx, Config{Model: "opus", Effort: "medium", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := sess.Send("kickoff"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainUntil(t, sess.Events(), stream.TypeResult)

	first := sess.Close()
	second := sess.Close()
	// Both calls return the same value because closeOnce gates the
	// real work and closeErr is never reassigned afterward.
	if first != second {
		t.Errorf("Close non-idempotent: first=%v second=%v", first, second)
	}
}

func TestSpawner_CancelDeliversSignal(t *testing.T) {
	sp := helperSpawner(t, "sigtrap")
	ctx, cancel := context.WithCancel(context.Background())
	sess, err := sp.Spawn(ctx, Config{Model: "opus", Effort: "medium", WorkDir: t.TempDir()})
	if err != nil {
		cancel()
		t.Fatalf("Spawn: %v", err)
	}
	// Wait for the helper to print its first event so we know its
	// signal handler is installed before we cancel.
	if ev := drainUntil(t, sess.Events(), stream.TypeSystem); ev == nil {
		cancel()
		_ = sess.Close()
		t.Fatal("did not observe system event before cancel")
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
	//   1. Helper trapped SIGINT and exited 7 cleanly. Signaled=false,
	//      Code=7. This is the documented happy path.
	//   2. WaitDelay's SIGKILL escalation reached the helper before
	//      it could exit on its own. Signaled=true, Signal=SIGKILL.
	//
	// Either way, the cancel reached the child — which is the
	// contract under test.
	switch {
	case !ee.Signaled && ee.Code == 7:
		// canonical case
	case ee.Signaled && (ee.Signal == syscall.SIGINT || ee.Signal == syscall.SIGKILL):
		// escalation case
	default:
		t.Errorf("unexpected exit shape: %+v", ee)
	}
}

func TestSpawner_HangingChildIsKilled(t *testing.T) {
	sp := helperSpawner(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := sp.Spawn(ctx, Config{Model: "opus", Effort: "medium", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cs, ok := sess.(*claudeSession)
	if !ok {
		t.Fatalf("expected *claudeSession, got %T", sess)
	}
	pid := cs.cmd.Process.Pid

	// Wait for the first event so the helper has reached its sleep.
	if ev := drainUntil(t, sess.Events(), stream.TypeSystem); ev == nil {
		_ = sess.Close()
		t.Fatal("did not observe system event")
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
