package main

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestWriteUsage_ContainsVersionAndDefaults(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writeUsage(&buf)
	out := buf.String()

	wants := []string{
		"ralph " + version,
		"USAGE",
		"DESCRIPTION",
		"FLAGS",
		"REQUIREMENT IDS",
		`"` + defaultReqs + `"`,
		// ralph is pi-exclusive: the manual names pi, not claude, and
		// carries no --engine/--effort rows.
		"pi",
		// The new project-layout section mentions the app-root
		// subdirectory by name.
		"app-root",
		// The --app-root flag is documented in the FLAGS section.
		"--app-root",
		// Q7 final flag surface: the three pi pass-throughs are
		// documented.
		"--provider=ID",
		"--model=NAME",
		"--thinking=LEVEL",
		// The default-tools behaviour is spelled out: an empty --tools
		// yields pi's full built-in allowlist.
		"--tools=LIST",
		"read,bash,edit,write,grep,find,",
		// pi is the validator for thinking; ralph applies no default.
		"off|minimal|low|medium|",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("writeUsage output missing %q", w)
		}
	}

	// The dead claude-era knobs must not survive anywhere in the manual,
	// nor any claude/stream-json/Anthropic wording in the flag prose.
	// The check is case-insensitive so no casing of the vendor wording
	// slips back in.
	lower := strings.ToLower(out)
	bannedSubstrings := []string{
		"--engine",
		"--config-dir",
		"--1m-context",
		"--one-m-context",
		"--enable-claudeai-mcp-servers",
		"--dangerously-skip-permissions",
		"--effort",
		"claude",
		"stream-json",
		"anthropic",
	}
	for _, b := range bannedSubstrings {
		if strings.Contains(lower, b) {
			t.Errorf("writeUsage output still mentions removed/claude wording %q", b)
		}
	}
}

func TestWriteUsagePaged_NonTTYWritesToWriter(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writeUsagePaged(&buf)
	out := buf.String()
	if !strings.Contains(out, "ralph "+version) {
		t.Errorf("writeUsagePaged should write directly to non-TTY writer; got %q", out)
	}
	if !strings.Contains(out, "REQUIREMENT IDS") {
		t.Errorf("writeUsagePaged: full manual not emitted, got %q", out)
	}
}

// TestWriteUsagePaged_PagedBranchViaPTY exercises the TTY path of
// writeUsagePaged by passing the master end of a fresh pseudo-terminal
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
	writeUsagePaged(master)

	// Closing the master signals EOF to the drain goroutine.
	master.Close()
	<-doneCh

	if !strings.Contains(collected.String(), "ralph "+version) {
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
