package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
)

// runCapture invokes run() with the given args, captures stdout/stderr,
// and returns (exitCode, stdout, stderr).
func runCapture(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunNoArgsIsUsageError(t *testing.T) {
	code, stdout, stderr := runCapture()
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if stdout != "" {
		t.Errorf("expected empty stdout on usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "USAGE") {
		t.Errorf("stderr should contain manual, got: %q", stderr)
	}
}

func TestRunVersion(t *testing.T) {
	for _, arg := range []string{"version", "-v", "--version"} {
		t.Run(arg, func(t *testing.T) {
			code, stdout, _ := runCapture(arg)
			if code != exitSuccess {
				t.Errorf("exit code = %d, want %d", code, exitSuccess)
			}
			want := "ralph " + version + "\n"
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			code, stdout, stderr := runCapture(arg)
			if code != exitSuccess {
				t.Errorf("exit code = %d, want %d", code, exitSuccess)
			}
			if !strings.Contains(stdout, "USAGE") || !strings.Contains(stdout, "REQUIREMENT IDS") {
				t.Errorf("help output missing expected sections: %q", stdout)
			}
			if stderr != "" {
				t.Errorf("expected empty stderr on help, got %q", stderr)
			}
		})
	}
}

func TestRunNewID(t *testing.T) {
	code, stdout, _ := runCapture("newid")
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	id := strings.TrimSuffix(stdout, "\n")
	if _, err := idgen.TimeOf(id); err != nil {
		t.Errorf("printed id %q is not canonical: %v", id, err)
	}
}

func TestRunNewIDRejectsExtraArgs(t *testing.T) {
	code, _, stderr := runCapture("newid", "extra")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "no arguments") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRunTimeOfRoundTrip(t *testing.T) {
	id := idgen.New()
	code, stdout, stderr := runCapture("time-of", id)
	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, exitSuccess, stderr)
	}
	out := strings.TrimSuffix(stdout, "\n")
	// Format: 2006-01-02T15:04:05.000Z — ASCII sanity check.
	if len(out) != len("2006-01-02T15:04:05.000Z") || !strings.HasSuffix(out, "Z") {
		t.Errorf("time-of output not in expected format: %q", out)
	}
}

func TestRunTimeOfRequiresExactlyOneArg(t *testing.T) {
	cases := [][]string{
		{"time-of"},
		{"time-of", "a", "b"},
	}
	for _, args := range cases {
		code, _, stderr := runCapture(args...)
		if code != exitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(stderr, "exactly one ID") {
			t.Errorf("%v: stderr = %q", args, stderr)
		}
	}
}

func TestRunTimeOfInvalidID(t *testing.T) {
	code, _, stderr := runCapture("time-of", "not-an-id")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "ralph:") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRunLoopRequiresWorkdir(t *testing.T) {
	// All flags consumed, no positional argument.
	code, _, stderr := runCapture("--model=sonnet")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "WORKDIR") {
		t.Errorf("stderr should mention WORKDIR, got %q", stderr)
	}
}

func TestRunLoopRejectsUnknownFlag(t *testing.T) {
	code, _, _ := runCapture("--definitely-not-a-flag", ".")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
}

func TestRunLoopRejectsExtraPositional(t *testing.T) {
	code, _, stderr := runCapture("workdir", "extra")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "WORKDIR") {
		t.Errorf("stderr = %q", stderr)
	}
}
