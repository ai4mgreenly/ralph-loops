//go:build unix && !pilive

package agent_test

import "testing"

// runLivePiSmoke is the no-`pilive`-tag stub. The default build (plain
// `make test`, CI, unauthed dev boxes) compiles only this version, so
// the live smoke test always skips cleanly here — it never fails, never
// shells out to pi, and never costs an API call. Building with
// `-tags pilive` swaps in the real implementation in live_on_test.go
// (which is itself still gated on RALPH_PI_LIVE=1 and `pi` on $PATH).
func runLivePiSmoke(t *testing.T) {
	t.Skip("live pi smoke test skipped: build with -tags pilive AND set RALPH_PI_LIVE=1 (needs pi authed on PATH) to run it; see live_test.go")
}
