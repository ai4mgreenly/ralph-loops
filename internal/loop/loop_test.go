package loop

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

func TestRun_RejectsEmptyConfig(t *testing.T) {
	err := Run(Config{})
	if err == nil {
		t.Fatal("expected error from empty Config, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "ReqsDir is required") {
		t.Errorf("expected ReqsDir mention in error, got %v", err)
	}
}

func TestRun_RejectsNegativeDuration(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Duration = -1 * time.Second
	err := Run(cfg)
	if err == nil {
		t.Fatal("expected error for negative duration")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestFormatBudget(t *testing.T) {
	if got := formatBudget(0); got != "unlimited" {
		t.Errorf("formatBudget(0) = %q, want \"unlimited\"", got)
	}
	if got := formatBudget(2 * time.Hour); got != "2h0m0s" {
		t.Errorf("formatBudget(2h) = %q", got)
	}
}

// TestCtxExit_TranslatesContextErrors covers the (panel-reason,
// returned-error) projection. The unreachable `default` panic is
// intentionally not exercised: ctxExit's contract is that callers
// only invoke it after observing ctx.Err() != nil, and the only
// sentinels Go produces from a context are context.Canceled and
// context.DeadlineExceeded.
func TestCtxExit_TranslatesContextErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantReason exitReason
		wantErr    error
	}{
		{
			name:       "deadline exceeded maps to timeout",
			err:        context.DeadlineExceeded,
			wantReason: exitTimedOut,
			wantErr:    ErrTimedOut,
		},
		{
			name:       "canceled maps to interrupted",
			err:        context.Canceled,
			wantReason: exitInterrupted,
			wantErr:    ErrInterrupted,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotReason, gotErr := ctxExit(tc.err)
			if gotReason != tc.wantReason {
				t.Errorf("reason = %v, want %v", gotReason, tc.wantReason)
			}
			if !errors.Is(gotErr, tc.wantErr) {
				t.Errorf("err = %v, want errors.Is %v", gotErr, tc.wantErr)
			}
		})
	}
}

// TestInstallForceQuit_SecondSIGINTCallsQuit drives a real second
// SIGINT and confirms quit(130) is invoked. This is the kind of test
// that's flaky if rushed, so we synchronize via a polling loop on the
// "graceful" context's Done channel before sending the second signal.
func TestInstallForceQuit_SecondSIGINTCallsQuit(t *testing.T) {
	// Not parallel: this test sends real SIGINT to the test process,
	// and parallel goroutines from other tests would observe the same
	// signal stream.

	sigCtx, stopSig := context.WithCancel(context.Background())

	var quitCode atomic.Int64
	quitCh := make(chan int, 1)
	quit := func(code int) {
		quitCode.Store(int64(code))
		select {
		case quitCh <- code:
		default:
		}
	}

	stop := installForceQuit(sigCtx, io.Discard, quit)
	defer stop()

	// First "interrupt" — we just cancel sigCtx directly to model the
	// first SIGINT having already been consumed by NotifyContext. This
	// puts the goroutine into its second-signal listening state without
	// racing the OS signal pipe.
	stopSig()

	// Now send a real SIGINT. installForceQuit registered its own
	// signal.Notify so it sees this directly.
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("kill self: %v", err)
	}

	select {
	case got := <-quitCh:
		if got != 130 {
			t.Errorf("quit called with %d, want 130", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quit was not called within timeout")
	}

	if got := quitCode.Load(); got != 130 {
		t.Errorf("quitCode = %d, want 130", got)
	}
}

func minimalValidConfig() Config {
	return Config{
		ReqsDir:  "reqs",
		WorkDir:  ".",
		Model:    "opus",
		Effort:   "medium",
		Duration: time.Hour,
		Prompt:   "operator prompt",
		Version:  "test",
		Theme:    ui.NewThemeWith(false, 0),
	}
}
