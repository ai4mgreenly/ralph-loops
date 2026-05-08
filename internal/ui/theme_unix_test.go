//go:build unix

package ui

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestWatchResize_StopFuncCleansUp asserts that calling stop()
// terminates the watcher goroutine and does not clobber a width set
// by the test before stopping. We can't synthesize a SIGWINCH that
// reliably re-detects to a known column count from a non-TTY test
// environment (detectWidth on /dev/null returns 0), so the contract
// we verify here is the lifecycle: SetWidth(99) -> stop() -> Width()
// remains 99.
func TestWatchResize_StopFuncCleansUp(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer f.Close()

	th := NewThemeWith(false, 0)
	stop := th.WatchResize(f)
	th.SetWidth(99)
	stop()
	// Calling stop twice would double-close; the contract says callers
	// defer once. Width should still be 99.
	if th.Width() != 99 {
		t.Errorf("Width after stop() = %d, want 99", th.Width())
	}
}

// TestWatchResize_NilTheme verifies that WatchResize on a nil Theme
// returns a non-nil no-op stop func.
func TestWatchResize_NilTheme(t *testing.T) {
	var th *Theme
	stop := th.WatchResize(os.Stdout)
	if stop == nil {
		t.Fatal("stop func should be non-nil")
	}
	stop() // must not panic
}

// TestWatchResize_GoroutineExits verifies that calling stop() releases
// the watcher goroutine: the live goroutine count after stop()+brief
// wait does not exceed the count before WatchResize was called.
func TestWatchResize_GoroutineExits(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer f.Close()

	before := runtime.NumGoroutine()
	th := NewThemeWith(false, 0)
	stop := th.WatchResize(f)
	stop()

	// Brief poll: the watcher's select needs a scheduling tick to drain
	// done and return.
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if runtime.NumGoroutine() > before {
		t.Errorf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
	}
}

// TestWatchResize_FiresOnSIGWINCH sends SIGWINCH to the current
// process and verifies the watcher goroutine processes it without
// crashing. We don't assert the new width value because detectWidth
// on /dev/null returns 0 — the test verifies the plumbing fires.
func TestWatchResize_FiresOnSIGWINCH(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer f.Close()

	th := NewThemeWith(false, 42)
	stop := th.WatchResize(f)
	defer stop()

	// Send SIGWINCH; the goroutine should observe it and call SetWidth
	// with detectWidth(/dev/null) == 0.
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Poll for width to drop from 42 to 0.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if th.Width() == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Errorf("watcher did not update width after SIGWINCH; Width=%d", th.Width())
}
