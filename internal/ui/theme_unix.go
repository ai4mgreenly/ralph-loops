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
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sigCh:
				t.SetWidth(detectWidth(out))
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
