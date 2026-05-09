package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// spinnerFrames is the standard 10-frame braille rotation used by
// many CLIs. Each frame is a single column-3-2 dot pattern; cycling
// at ~100ms gives a smooth rotation without distracting motion.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

const (
	spinnerDefaultDelay = 3 * time.Second
	spinnerDefaultTick  = 100 * time.Millisecond
)

// eraseLine returns the ANSI sequence that moves the cursor to the
// start of the current line and clears it: `\r` + ESC[2K. Both are
// safe on any terminal that already accepts the colour escapes ralph
// emits elsewhere.
const eraseLine = "\r\x1b[2K"

// clock is the wall-clock seam used by [Spinner]. The production
// implementation, [realClock], delegates to the time package; tests
// can substitute a controlled implementation to advance paint timing
// deterministically.
//
// NewTicker returns the tick channel together with a stop function so
// the spinner can release the underlying goroutine when it shuts down,
// without forcing the abstraction to leak [time.Ticker]'s concrete
// type to test doubles.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) (<-chan time.Time, func())
}

// realClock implements [clock] against the standard library.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Spinner displays a braille rotator on the line where the next piece
// of output would appear, annotated with a "waiting for X (Hh Mm Ss)"
// label. It paints from a goroutine that the caller starts and stops;
// when stdout is not a terminal the spinner is a no-op so piped output
// stays clean.
//
// One Spinner is meant to be reused across many wait intervals: each
// Start/Stop pair brackets one wait. Calling Start while already
// running, or Stop while not running, is a no-op.
type Spinner struct {
	out      io.Writer
	label    string
	delay    time.Duration
	tick     time.Duration
	enabled  bool
	useColor bool
	clk      clock

	running atomic.Bool
	start   time.Time
	stopCh  chan struct{}
	doneCh  chan struct{}

	// paintCh, when non-nil, receives one empty value after every
	// paint and once when the goroutine reaches its post-pre-roll
	// blocking select (so tests can synchronize on goroutine state
	// without polling). Production code never sets it.
	paintCh chan<- struct{}
}

// NewSpinner returns a Spinner that paints to out, prefixed with
// label, after a 3 second pre-roll — long enough that brief waits
// leave no trace on the terminal. The first painted frame already
// shows the elapsed-time annotation as "3s". If out is not a
// terminal the spinner is permanently disabled. useColor controls
// whether the painted line is wrapped in dim-grey ANSI escapes;
// callers normally pass [Theme.UseColor].
func NewSpinner(out io.Writer, label string, useColor bool) *Spinner {
	return &Spinner{
		out:      out,
		label:    label,
		delay:    spinnerDefaultDelay,
		tick:     spinnerDefaultTick,
		enabled:  IsTTY(out),
		useColor: useColor,
		clk:      realClock{},
	}
}

// withClock swaps in an alternative [clock] implementation. Intended
// for tests that need deterministic paint timing; production code
// keeps the [realClock] installed by [NewSpinner]. Returns the
// receiver so tests can chain configuration calls.
func (s *Spinner) withClock(c clock) *Spinner { //nolint:unparam // fluent test seam
	s.clk = c
	return s
}

// withPaintCh installs a notification channel that receives one
// empty value after each paint (and once after the pre-roll select
// becomes ready for an After fire). Used by tests to synchronize on
// goroutine progression without polling. Returns the receiver so
// tests can chain configuration calls.
func (s *Spinner) withPaintCh(ch chan<- struct{}) *Spinner { //nolint:unparam // fluent test seam
	s.paintCh = ch
	return s
}

// notifyPaint signals one progress notch on s.paintCh. Non-blocking:
// a slow test consumer never wedges the spinner.
func (s *Spinner) notifyPaint() {
	if s.paintCh == nil {
		return
	}
	select {
	case s.paintCh <- struct{}{}:
	default:
	}
}

// Start begins a new wait interval. The spinner pre-rolls for the
// configured delay before the first paint, so short waits leave no
// trace on the terminal.
func (s *Spinner) Start() {
	if !s.enabled {
		return
	}
	if !s.running.CompareAndSwap(false, true) {
		return
	}
	s.start = s.clk.Now()
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	go s.run()
}

// Stop halts the painting goroutine and erases any spinner line it
// drew. Safe to call when Start was never called or after a previous
// Stop.
func (s *Spinner) Stop() {
	if !s.running.CompareAndSwap(true, false) {
		return
	}
	close(s.stopCh)
	<-s.doneCh
}

func (s *Spinner) run() {
	defer close(s.doneCh)

	// Pre-roll: don't paint anything at all until the wait is long
	// enough to be worth annotating.
	preRoll := s.clk.After(s.delay)
	s.notifyPaint() // tests: After is now installed
	select {
	case <-s.stopCh:
		return
	case <-preRoll:
	}

	frame := 0
	paint := func() {
		fmt.Fprintf(s.out, "%s%s%c %s (%s)%s",
			eraseLine,
			s.dimIfColor(),
			spinnerFrames[frame],
			s.label,
			formatElapsed(s.clk.Now().Sub(s.start)),
			s.resetIfColor(),
		)
		frame = (frame + 1) % len(spinnerFrames)
	}
	paint()
	s.notifyPaint() // tests: first paint complete

	tickCh, tickStop := s.clk.NewTicker(s.tick)
	defer tickStop()
	s.notifyPaint() // tests: ticker installed
	for {
		select {
		case <-s.stopCh:
			fmt.Fprint(s.out, eraseLine)
			return
		case <-tickCh:
			paint()
			s.notifyPaint() // tests: tick paint complete
		}
	}
}

// formatElapsed renders d as "Xh Ym Zs", omitting any leading
// component that is zero. A zero duration formats as "0s".
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	sec := total % 60

	parts := make([]string, 0, 3)
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if sec > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", sec))
	}
	return strings.Join(parts, " ")
}

// IsTTY reports whether w is a character device. Returns false for
// any writer that is not an *os.File, including bytes.Buffer and
// pipes — a deliberately conservative choice: anything we can't
// confirm is a real terminal is treated as "don't emit cursor
// movement".
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func (s *Spinner) dimIfColor() string {
	if s.useColor {
		return dimEscape
	}
	return ""
}

func (s *Spinner) resetIfColor() string {
	if s.useColor {
		return resetEscape
	}
	return ""
}
