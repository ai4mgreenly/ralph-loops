package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeBuffer is a goroutine-safe wrapper around bytes.Buffer used by
// the spinner tests so the test goroutine and the paint goroutine
// don't race on the underlying buffer's internal state.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// compile-time check that safeBuffer is a writer.
var _ io.Writer = (*safeBuffer)(nil)

// fakeClock is a deterministic [clock] used by the spinner tests so
// paint timing isn't tied to the wall clock. Tests advance time via
// [fakeClock.advance] and [fakeClock.fireAfter] / [fakeClock.tick] to
// drive the spinner's pre-roll and ticker channels.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	afterCh chan time.Time // pending After() channel; tests close to fire it
	tickCh  chan time.Time // pending NewTicker channel; tests send to fire
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After hands the spinner a channel the test will fire when it wants
// the pre-roll to end. Only one outstanding After is supported, which
// matches the spinner's actual usage pattern.
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.afterCh = ch
	return ch
}

// NewTicker hands the spinner a channel the test will manually drive
// to model paint ticks. Only one outstanding ticker is supported.
func (c *fakeClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.tickCh = ch
	stop := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.tickCh = nil
	}
	return ch, stop
}

// advance moves fake time forward; subsequent Now() calls observe the
// new instant. Used together with [fakeClock.firePreRoll] /
// [fakeClock.fireTick] to drive deterministic elapsed-time annotations.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// reset clears the pending After/Ticker channels so a subsequent
// waitForAfter / waitForTicker observes the next iteration's setup
// rather than a stale entry from a previous Start/Stop cycle.
func (c *fakeClock) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.afterCh = nil
	c.tickCh = nil
}

// firePreRoll fires the After() channel set up by the spinner's
// goroutine. Returns false if the spinner hasn't called After yet.
func (c *fakeClock) firePreRoll() bool {
	c.mu.Lock()
	ch := c.afterCh
	now := c.now
	c.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- now:
		return true
	default:
		return false
	}
}

// fireTick fires one tick on the NewTicker channel set up by the
// spinner. Returns false if NewTicker hasn't been called yet.
func (c *fakeClock) fireTick() bool {
	c.mu.Lock()
	ch := c.tickCh
	now := c.now
	c.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- now:
		return true
	default:
		return false
	}
}

// awaitPaint blocks on paintCh for one progress notch, failing the
// test on timeout. Replaces the millisecond-poll loop the suite used
// before the [Spinner] grew a paint-notification seam.
func awaitPaint(t *testing.T, paintCh <-chan struct{}) {
	t.Helper()
	select {
	case <-paintCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for paint notification")
	}
}

// drainPaint pulls every queued paint notification off ch without
// blocking. Used by [TestSpinner_StartStopReusable] between cycles
// so a stale notch from the previous Start/Stop doesn't be consumed
// by the next iteration's awaitPaint.
func drainPaint(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestSpinner_FormatElapsed_AllRanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{500 * time.Millisecond, "0s"},
		{1 * time.Second, "1s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{61 * time.Second, "1m 1s"},
		{90 * time.Second, "1m 30s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{1 * time.Hour, "1h"},
		{1*time.Hour + 30*time.Second, "1h 30s"},
		{1*time.Hour + 3*time.Minute + 11*time.Second, "1h 3m 11s"},
		{2 * time.Hour, "2h"},
		{-5 * time.Second, "0s"},
	}
	for _, tc := range tests {
		if got := formatElapsed(tc.in); got != tc.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSpinner_DisabledWhenNotTTY(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	s := NewSpinner(&buf, "waiting", false)
	if s.enabled {
		t.Fatal("spinner should auto-disable for non-*os.File writer")
	}
	// A disabled spinner is a no-op: Start/Stop must not write anything,
	// and the test does not need to wait for any goroutine.
	s.Start()
	s.Stop()
	if buf.Len() != 0 {
		t.Errorf("disabled spinner wrote %q", buf.String())
	}
}

// TestSpinner_SilentBeforeDelay drives the pre-roll path without
// firing After: the spinner must paint nothing and Stop must clean up
// without hanging.
func TestSpinner_SilentBeforeDelay(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clk := newFakeClock(time.Unix(0, 0).UTC())
	paintCh := make(chan struct{}, 8)

	s := NewSpinner(buf, "waiting", false)
	s.enabled = true
	_ = s.withClock(clk)
	_ = s.withPaintCh(paintCh)

	s.Start()
	// Wait for the goroutine to install After (paint notch #1) so
	// Stop doesn't win the race before the goroutine hits its select.
	awaitPaint(t, paintCh)
	s.Stop()

	if buf.Len() != 0 {
		t.Errorf("spinner painted before pre-roll elapsed: %q", buf.String())
	}
}

// TestSpinner_PaintsAfterPreRoll fires the pre-roll channel and one
// tick; the buffer should contain the label, a frame, an elapsed
// annotation, and end with the erase sequence.
func TestSpinner_PaintsAfterPreRoll(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clk := newFakeClock(time.Unix(0, 0).UTC())
	paintCh := make(chan struct{}, 8)

	s := NewSpinner(buf, "waiting for claude", false)
	s.enabled = true
	_ = s.withClock(clk)
	_ = s.withPaintCh(paintCh)

	s.Start()
	// Notch 1: After installed.
	awaitPaint(t, paintCh)
	clk.advance(s.delay)
	if !clk.firePreRoll() {
		t.Fatal("pre-roll fire returned false")
	}
	// Notch 2: first paint complete. Notch 3: ticker installed.
	awaitPaint(t, paintCh)
	awaitPaint(t, paintCh)

	s.Stop()

	out := buf.String()
	if !strings.Contains(out, "waiting for claude") {
		t.Errorf("output missing label: %q", out)
	}
	if !strings.ContainsAny(out, string(spinnerFrames)) {
		t.Errorf("output missing any braille frame: %q", out)
	}
	// 3s elapsed because we advanced by spinnerDefaultDelay (3s).
	if !strings.Contains(out, "(3s)") {
		t.Errorf("output missing elapsed-time annotation (3s): %q", out)
	}
	if !strings.HasSuffix(out, eraseLine) {
		t.Errorf("output should end with the erase sequence, got tail %q", tail(out, 16))
	}
}

// TestSpinner_StopBeforeFirstPaint exercises the path where Stop
// arrives before the pre-roll channel has fired — nothing should be
// painted and the goroutine should exit cleanly.
func TestSpinner_StopBeforeFirstPaint(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clk := newFakeClock(time.Unix(0, 0).UTC())
	paintCh := make(chan struct{}, 8)

	s := NewSpinner(buf, "waiting", false)
	s.enabled = true
	_ = s.withClock(clk)
	_ = s.withPaintCh(paintCh)

	s.Start()
	awaitPaint(t, paintCh) // After installed
	s.Stop()               // before firing pre-roll

	if buf.Len() != 0 {
		t.Errorf("spinner stopped before paint should leave nothing on stdout, got %q", buf.String())
	}
}

// TestSpinner_StartStopReusable exercises the reuse contract: a
// Spinner accepts repeated Start/Stop pairs and continues to paint
// labels under a fake clock.
func TestSpinner_StartStopReusable(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clk := newFakeClock(time.Unix(0, 0).UTC())
	paintCh := make(chan struct{}, 8)

	s := NewSpinner(buf, "x", false)
	s.enabled = true
	_ = s.withClock(clk)
	_ = s.withPaintCh(paintCh)

	for i := 0; i < 3; i++ {
		clk.reset()
		drainPaint(paintCh)
		s.Start()
		awaitPaint(t, paintCh) // After installed
		clk.advance(s.delay)
		if !clk.firePreRoll() {
			t.Fatalf("iteration %d: pre-roll fire returned false", i)
		}
		awaitPaint(t, paintCh) // first paint
		awaitPaint(t, paintCh) // ticker installed
		s.Stop()
	}
	if !strings.Contains(buf.String(), "x") {
		t.Errorf("expected label across multiple cycles, got %q", buf.String())
	}
}

// TestSpinner_TickAdvancesFrame fires multiple ticks and asserts the
// frame index rotates: the painted output should contain at least two
// distinct braille frames.
func TestSpinner_TickAdvancesFrame(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clk := newFakeClock(time.Unix(0, 0).UTC())
	paintCh := make(chan struct{}, 8)

	s := NewSpinner(buf, "x", false)
	s.enabled = true
	_ = s.withClock(clk)
	_ = s.withPaintCh(paintCh)

	s.Start()
	awaitPaint(t, paintCh) // After installed
	clk.advance(s.delay)
	clk.firePreRoll()
	awaitPaint(t, paintCh) // first paint
	awaitPaint(t, paintCh) // ticker installed

	// Drive a few ticks; each tick produces a paint notch.
	for i := 0; i < 3; i++ {
		clk.advance(s.tick)
		if !clk.fireTick() {
			t.Fatalf("tick %d: fire returned false", i)
		}
		awaitPaint(t, paintCh)
	}
	s.Stop()

	out := buf.String()
	// Count distinct frames seen. With at least 4 paints (initial +
	// 3 ticks) we should observe at least two different frames.
	seen := make(map[rune]bool)
	for _, r := range out {
		for _, f := range spinnerFrames {
			if r == f {
				seen[r] = true
			}
		}
	}
	if len(seen) < 2 {
		t.Errorf("ticks did not advance the frame: saw %d distinct frames in %q", len(seen), out)
	}
}

func TestIsTTY_BufferIsNotTTY(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if IsTTY(&buf) {
		t.Error("bytes.Buffer is not a TTY")
	}
}

// TestIsTTY_NonCharDeviceFile covers the path where w is an *os.File
// but the file is not a character device (a regular file). IsTTY
// must return false.
func TestIsTTY_NonCharDeviceFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f, err := os.Create(dir + "/out")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if IsTTY(f) {
		t.Error("regular file is not a character device; IsTTY should be false")
	}
}

// TestIsTTY_StatErrorOnClosedFile drives the err != nil branch of
// IsTTY: a closed *os.File can't be Stat'd, so IsTTY must return
// false instead of panicking.
func TestIsTTY_StatErrorOnClosedFile(t *testing.T) {
	t.Parallel()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Close()
	if IsTTY(f) {
		t.Error("closed file should not be reported as TTY")
	}
}

// TestRealClock_WiresThroughTime sanity-checks the production
// [clock] implementation: Now returns a recent instant, After fires
// in finite time, and NewTicker delivers a tick that we can stop.
// Replaces the polling-based smoke test the spinner suite used
// before the paintCh seam landed.
func TestRealClock_WiresThroughTime(t *testing.T) {
	t.Parallel()
	c := realClock{}
	if c.Now().IsZero() {
		t.Error("realClock.Now should never return zero")
	}
	select {
	case <-c.After(2 * time.Millisecond):
	case <-time.After(time.Second):
		t.Fatal("realClock.After did not fire")
	}
	tickCh, stop := c.NewTicker(1 * time.Millisecond)
	defer stop()
	select {
	case <-tickCh:
	case <-time.After(time.Second):
		t.Fatal("realClock.NewTicker did not fire")
	}
}

// TestSpinner_DoubleStartIgnored covers the running.CompareAndSwap
// false branch in Start: a second Start call while the spinner is
// already running must be a no-op.
func TestSpinner_DoubleStartIgnored(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clk := newFakeClock(time.Unix(0, 0).UTC())
	paintCh := make(chan struct{}, 8)

	s := NewSpinner(buf, "x", false)
	s.enabled = true
	_ = s.withClock(clk)
	_ = s.withPaintCh(paintCh)

	s.Start()
	awaitPaint(t, paintCh)
	// Second Start should short-circuit and NOT clobber stopCh/doneCh.
	s.Start()
	s.Stop()
}

// TestSpinner_PaintsWithColor exercises dimIfColor and resetIfColor
// when useColor is true: the painted output should be wrapped in the
// dim ANSI escape and the reset escape.
func TestSpinner_PaintsWithColor(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clk := newFakeClock(time.Unix(0, 0).UTC())
	paintCh := make(chan struct{}, 8)

	s := NewSpinner(buf, "wait", true) // useColor=true
	s.enabled = true
	_ = s.withClock(clk)
	_ = s.withPaintCh(paintCh)

	s.Start()
	awaitPaint(t, paintCh) // After installed
	clk.advance(s.delay)
	clk.firePreRoll()
	awaitPaint(t, paintCh) // first paint
	awaitPaint(t, paintCh) // ticker installed
	s.Stop()

	out := buf.String()
	if !strings.Contains(out, dimEscape) {
		t.Errorf("colored spinner missing dim escape, got %q", out)
	}
	if !strings.Contains(out, resetEscape) {
		t.Errorf("colored spinner missing reset escape, got %q", out)
	}
}

// TestSpinner_DimResetHelpers covers the false branches of
// dimIfColor / resetIfColor: when useColor is off they must return
// the empty string.
func TestSpinner_DimResetHelpers(t *testing.T) {
	t.Parallel()
	off := &Spinner{useColor: false}
	if off.dimIfColor() != "" {
		t.Errorf("dimIfColor with useColor=false should be empty, got %q", off.dimIfColor())
	}
	if off.resetIfColor() != "" {
		t.Errorf("resetIfColor with useColor=false should be empty, got %q", off.resetIfColor())
	}
	on := &Spinner{useColor: true}
	if on.dimIfColor() != dimEscape {
		t.Errorf("dimIfColor with useColor=true = %q, want %q", on.dimIfColor(), dimEscape)
	}
	if on.resetIfColor() != resetEscape {
		t.Errorf("resetIfColor with useColor=true = %q, want %q", on.resetIfColor(), resetEscape)
	}
}

// TestSpinner_DoubleStopIgnored covers the running.CompareAndSwap
// false branch in Stop: a second Stop call while the spinner is
// already stopped must be a no-op.
func TestSpinner_DoubleStopIgnored(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	s := NewSpinner(buf, "x", false)
	// Never started — Stop should short-circuit cleanly.
	s.Stop()
	s.Stop()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
