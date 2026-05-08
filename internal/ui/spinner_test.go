package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSpinnerFormatElapsed(t *testing.T) {
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

func TestSpinnerDisabledWhenNotTTY(t *testing.T) {
	var buf bytes.Buffer
	s := NewSpinner(&buf, "waiting")
	if s.enabled {
		t.Fatal("spinner should auto-disable for non-*os.File writer")
	}
	s.Start()
	time.Sleep(20 * time.Millisecond)
	s.Stop()
	if buf.Len() != 0 {
		t.Errorf("disabled spinner wrote %q", buf.String())
	}
}

func TestSpinnerSilentBeforeDelay(t *testing.T) {
	var buf bytes.Buffer
	s := NewSpinner(&buf, "waiting")
	s.enabled = true
	s.delay = 100 * time.Millisecond
	s.tick = 10 * time.Millisecond

	s.Start()
	time.Sleep(20 * time.Millisecond) // well under the 100ms pre-roll
	s.Stop()
	if buf.Len() != 0 {
		t.Errorf("spinner painted before pre-roll elapsed: %q", buf.String())
	}
}

func TestSpinnerPaintsAfterDelay(t *testing.T) {
	prev := useColor
	SetColor(false)
	defer SetColor(prev)

	var buf bytes.Buffer
	s := NewSpinner(&buf, "waiting for claude")
	s.enabled = true
	s.delay = 10 * time.Millisecond
	s.tick = 10 * time.Millisecond

	s.Start()
	time.Sleep(80 * time.Millisecond)
	s.Stop()

	out := buf.String()
	if !strings.Contains(out, "waiting for claude") {
		t.Errorf("output missing label: %q", out)
	}
	if !strings.ContainsAny(out, string(spinnerFrames)) {
		t.Errorf("output missing any braille frame: %q", out)
	}
	if !strings.Contains(out, "(0s)") && !strings.Contains(out, "0s)") {
		t.Errorf("output missing elapsed-time annotation: %q", out)
	}
	// Final write should erase the spinner line.
	if !strings.HasSuffix(out, eraseLine) {
		t.Errorf("output should end with the erase sequence, got tail %q", tail(out, 16))
	}
}

func TestSpinnerFirstPaintShowsDelayAsElapsed(t *testing.T) {
	prev := useColor
	SetColor(false)
	defer SetColor(prev)

	var buf bytes.Buffer
	s := NewSpinner(&buf, "waiting")
	s.enabled = true
	// Use a known whole-second delay so we can assert the elapsed
	// rendering exactly. The default is 3s; any whole-second delay
	// exercises the same property: when the goroutine first paints,
	// time.Since(start) >= delay, so int seconds == delay seconds.
	s.delay = 1 * time.Second
	s.tick = 50 * time.Millisecond

	s.Start()
	// Sleep just past the delay; one paint should land before we stop.
	time.Sleep(s.delay + 30*time.Millisecond)
	s.Stop()

	out := buf.String()
	if !strings.Contains(out, "(1s)") {
		t.Errorf("first paint should show elapsed = delay (1s), got %q", out)
	}
}

func TestSpinnerStopBeforeFirstPaint(t *testing.T) {
	var buf bytes.Buffer
	s := NewSpinner(&buf, "waiting")
	s.enabled = true
	s.delay = 100 * time.Millisecond
	s.tick = 10 * time.Millisecond

	s.Start()
	s.Stop() // immediate stop, well before delay
	if buf.Len() != 0 {
		t.Errorf("spinner stopped before paint should leave nothing on stdout, got %q", buf.String())
	}
}

func TestSpinnerStartStopReusable(t *testing.T) {
	prev := useColor
	SetColor(false)
	defer SetColor(prev)

	var buf bytes.Buffer
	s := NewSpinner(&buf, "x")
	s.enabled = true
	s.delay = 5 * time.Millisecond
	s.tick = 5 * time.Millisecond

	for i := 0; i < 3; i++ {
		s.Start()
		time.Sleep(20 * time.Millisecond)
		s.Stop()
	}
	if !strings.Contains(buf.String(), "x") {
		t.Errorf("expected label across multiple cycles, got %q", buf.String())
	}
}

func TestIsTTY_BufferIsNotTTY(t *testing.T) {
	var buf bytes.Buffer
	if IsTTY(&buf) {
		t.Error("bytes.Buffer is not a TTY")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
