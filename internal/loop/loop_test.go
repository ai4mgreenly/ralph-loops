package loop

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

func TestRun_RejectsEmptyConfig(t *testing.T) {
	err := Run(context.Background(), Config{})
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
	err := Run(context.Background(), cfg, WithDuration(-1*time.Second))
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

// blockingSpawner returns a [Session] whose Events reader blocks
// until the spawn-context fires; the reader then returns EOF and
// the loop observes the parent ctx.Err to classify the exit.
// Used to drive Run's timeout / interrupt paths without busy-waiting.
type blockingSpawner struct{}

func (blockingSpawner) Spawn(ctx context.Context, _ agent.Config) (Session, error) {
	pr, pw := io.Pipe()
	go func() {
		<-ctx.Done()
		_ = pw.Close()
	}()
	return &fakeSession{
		spawner: &fakeSpawner{},
		reader:  stream.NewReader(pr),
	}, nil
}

func TestRun_TimeoutReturnsErrTimedOut(t *testing.T) {
	cfg := minimalValidConfig()
	tmp := t.TempDir()
	err := Run(context.Background(), cfg,
		WithDuration(50*time.Millisecond),
		WithSpawner(blockingSpawner{}),
		WithResultsHome(tmp),
	)
	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("expected ErrTimedOut, got %v", err)
	}

	body, ferr := os.ReadFile(filepath.Join(tmp, "results.jsonl"))
	if ferr != nil {
		t.Fatalf("read jsonl: %v", ferr)
	}
	if !strings.Contains(string(body), `"exit":"timeout"`) {
		t.Errorf("expected exit=timeout in jsonl, got: %s", body)
	}
}

func TestRun_CancelReturnsErrInterrupted(t *testing.T) {
	cfg := minimalValidConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel before Run begins
	err := Run(ctx, cfg,
		WithSpawner(blockingSpawner{}),
		WithResultsHome(t.TempDir()),
	)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected ErrInterrupted, got %v", err)
	}
}

func TestRun_WritesResultsJSONL(t *testing.T) {
	cfg := minimalValidConfig()
	tmp := t.TempDir()
	sp := &fakeSpawner{scripts: [][]byte{[]byte(doneScript)}}
	err := Run(context.Background(), cfg,
		WithSpawner(sp),
		WithResultsHome(tmp),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body, ferr := os.ReadFile(filepath.Join(tmp, "results.jsonl"))
	if ferr != nil {
		t.Fatalf("read jsonl: %v", ferr)
	}
	if !strings.Contains(string(body), `"exit":"done"`) {
		t.Errorf("expected exit=done, got: %s", body)
	}
	if n := strings.Count(strings.TrimRight(string(body), "\n"), "\n"); n != 0 {
		t.Errorf("expected exactly one record line, found %d extras", n)
	}
}

func minimalValidConfig() Config {
	return Config{
		ReqsDir: "reqs",
		WorkDir: ".",
		Prompt:  "operator prompt",
		Theme:   ui.NewThemeWith(false, 0),
	}
}

// withDuration is a test helper that produces an [options] value with
// [defaultOptions] plus a wall-clock budget. The runWith kernel takes
// resolved options as a struct (not as []Option), so tests build the
// struct directly rather than going through the public Option API.
func withDuration(d time.Duration) options {
	o := defaultOptions()
	o.duration = d
	return o
}
