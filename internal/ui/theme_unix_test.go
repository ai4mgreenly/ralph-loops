//go:build unix

package ui

import (
	"os"
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
// the watcher goroutine. We use a sentinel channel closed by the
// watcher just before it returns rather than runtime.NumGoroutine,
// which is famously flaky as a leak detector.
func TestWatchResize_GoroutineExits(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer f.Close()

	exited := make(chan struct{})
	th := NewThemeWith(false, 0)
	stop := th.watchResizeWithHooks(f, nil, exited)
	stop()

	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("watcher goroutine did not exit after stop()")
	}
}

// TestWatchResize_FiresOnSIGWINCH sends SIGWINCH to the current
// process and verifies the watcher goroutine processes it without
// crashing. We synchronize on a hook channel the watcher signals
// after every resize handle, replacing the millisecond-poll loop the
// previous version of this test used.
func TestWatchResize_FiresOnSIGWINCH(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer f.Close()

	resized := make(chan struct{}, 1)
	th := NewThemeWith(false, 42)
	stop := th.watchResizeWithHooks(f, resized, nil)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-resized:
	case <-time.After(time.Second):
		t.Fatal("watcher did not handle SIGWINCH within timeout")
	}
	if th.Width() != 0 {
		t.Errorf("watcher did not update width after SIGWINCH; Width=%d", th.Width())
	}
}
