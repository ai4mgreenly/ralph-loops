//go:build !unix

package ui

import "os"

// WatchResize is a no-op on non-unix platforms (notably Windows),
// where SIGWINCH is unavailable. The returned stop func does nothing
// so callers can defer it unconditionally.
func (t *Theme) WatchResize(out *os.File) (stop func()) {
	return func() {}
}
