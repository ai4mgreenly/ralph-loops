//go:build unix

package ui

import (
	"os"
	"os/signal"
	"syscall"
)

// WatchResize installs a SIGWINCH handler that re-detects the
// terminal width whenever the controlling terminal is resized.
// Subsequent calls to [Theme.Width] (and the wrap math driven from
// it) observe the new value, so output rendered after the resize
// honours the new width. Output already on screen is not redrawn.
//
// The returned function uninstalls the handler and stops the
// background goroutine; callers should defer it.
func (t *Theme) WatchResize(out *os.File) (stop func()) {
	if t == nil {
		return func() {}
	}
	return t.watchResizeWithHooks(out, nil, nil)
}

// watchResizeWithHooks is the test-friendly form of [Theme.WatchResize].
// onResize, when non-nil, is invoked from the watcher goroutine after
// every SIGWINCH-driven SetWidth call. exited, when non-nil, is closed
// by the goroutine just before it returns. Production code uses
// [Theme.WatchResize], which passes nil for both hooks.
func (t *Theme) watchResizeWithHooks(out *os.File, onResize, exited chan<- struct{}) (stop func()) {
	if t == nil {
		return func() {}
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		if exited != nil {
			defer close(exited)
		}
		for {
			select {
			case <-sigCh:
				t.SetWidth(detectWidth(out))
				if onResize != nil {
					select {
					case onResize <- struct{}{}:
					default:
					}
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}
