//go:build unix

package agent_test

import "testing"

// TestLive_PiSmoke is the Q14(b) gated live smoke test: the pi-0.x
// event-format-drift early-warning. It spawns the REAL `pi` binary in
// one-shot `pi -p --mode json` print mode against live oauth and
// asserts the full kickoff → agent_end → parsed RALPH-STATUS sentinel
// path still works end to end. pi's 0.x event vocabulary moves fast;
// this test fails loudly the moment a capture-free format change breaks
// the decoder, before the (frozen) fixture corpus would catch it.
//
// It is DOUBLE-gated so CI and unauthed/dev environments always skip
// cleanly (never fail, never cost API calls):
//
//   - the `pilive` build tag must be set; AND
//   - the RALPH_PI_LIVE=1 environment variable must be set; AND
//   - the `pi` binary must be resolvable on $PATH.
//
// Any gate unmet ⇒ t.Skip with a clear reason. The default `make test`
// build (no `pilive` tag, RALPH_PI_LIVE unset) compiles only the
// no-tag stub in live_off_test.go, so the suite stays green regardless
// of environment; the real implementation lives in live_on_test.go and
// is compiled only under `-tags pilive`.
//
// To run it (costs real API calls; needs `pi` authed via
// ~/.pi/agent/auth.json):
//
//	RALPH_PI_LIVE=1 go test -tags pilive ./internal/agent/ \
//	    -run TestLive_PiSmoke -v
//
// The build-tag-free skip proof (CI shape) is:
//
//	go test ./internal/agent/ -run TestLive_PiSmoke -v
//
// which reports a SKIP because, without the `pilive` tag, only the stub
// runLivePiSmoke is compiled.
func TestLive_PiSmoke(t *testing.T) {
	runLivePiSmoke(t)
}
