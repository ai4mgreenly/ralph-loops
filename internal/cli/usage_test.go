package cli

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func sampleDefaults() UsageDefaults {
	return UsageDefaults{
		Version:         "1.2.3",
		Reqs:            "./reqs",
		Model:           "opus",
		Effort:          "medium",
		OneMContext:     true,
		SkipPermissions: true,
		ClaudeAIMCP:     false,
		OutputLines:     200,
	}
}

func TestWriteUsage_ContainsVersionAndDefaults(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	d := sampleDefaults()
	WriteUsage(&buf, d)
	out := buf.String()

	wants := []string{
		"ralph 1.2.3",
		"USAGE",
		"DESCRIPTION",
		"FLAGS",
		"REQUIREMENT IDS",
		`"./reqs"`,
		`"opus"`,
		`"medium"`,
		"200",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("WriteUsage output missing %q", w)
		}
	}
}

func TestWriteUsagePaged_NonTTYWritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	WriteUsagePaged(&buf, sampleDefaults())
	out := buf.String()
	if !strings.Contains(out, "ralph 1.2.3") {
		t.Errorf("WriteUsagePaged should write directly to non-TTY writer; got %q", out)
	}
	if !strings.Contains(out, "REQUIREMENT IDS") {
		t.Errorf("WriteUsagePaged: full manual not emitted, got %q", out)
	}
}

// TestWriteUsagePaged_PagedBranchViaPTY exercises the TTY path of
// WriteUsagePaged by passing the master end of a fresh pseudo-terminal
// as stdout. The master is a character device so ui.IsTTY accepts it,
// driving the os/exec spawn. PAGER=cat is set so the spawned pager is
// a deterministic builtin: it copies stdin straight to the master pty.
// We drain the pty in a goroutine so the pager's writes don't block.
//
// This test is unix-only (the pty mechanism doesn't exist on Windows)
// and is skipped when /dev/ptmx is unavailable or PAGER=cat can't be
// resolved on PATH.
func TestWriteUsagePaged_PagedBranchViaPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pty branch is unix-only")
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	defer master.Close()

	// Drain the pty master so the pager's writes don't block. The
	// drain goroutine exits when the master is closed.
	collected := &collectingWriter{}
	doneCh := make(chan struct{})
	go func() {
		_, _ = io.Copy(collected, master)
		close(doneCh)
	}()

	t.Setenv("PAGER", "cat")
	WriteUsagePaged(master, sampleDefaults())

	// Closing the master signals EOF to the drain goroutine.
	master.Close()
	<-doneCh

	if !strings.Contains(collected.String(), "ralph 1.2.3") {
		t.Errorf("pty-routed paged output missing version banner: %q", collected.String())
	}
}

// collectingWriter is a minimal goroutine-safe sink for the pty drain.
type collectingWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *collectingWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *collectingWriter) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
