package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// claudeBinary is the executable name the production spawner runs.
// Tests can construct a Spawner with a different binary to exercise
// process plumbing without invoking the real CLI.
const claudeBinary = "claude"

// waitDelay caps how long [exec.Cmd.Wait] will block after the
// context cancels and SIGINT has been delivered. After this elapses
// the runtime escalates to SIGKILL on the process group.
const waitDelay = 10 * time.Second

// closeGrace is the upper bound [Session.Close] waits for the
// stdout-drain goroutine to reach EOF before escalating to SIGKILL on
// the process. It exists so a child stuck mid-write cannot wedge the
// loop forever; five seconds is comfortably longer than any tool
// response observed in practice.
const closeGrace = 5 * time.Second

// NewSpawner returns the production [*Spawner] that runs the `claude`
// CLI from $PATH. Each Spawn invokes the binary anew; nothing is
// cached between iterations.
func NewSpawner() *Spawner {
	return &Spawner{binary: claudeBinary, Stderr: os.Stderr}
}

// newSpawnerWithBinary is the test seam used by integration tests in
// this package: it returns a [*Spawner] that runs the named binary
// (typically the test binary itself in re-exec mode) instead of the
// production claude CLI. Production code constructs spawners via
// [NewSpawner] and never sees this constructor.
func newSpawnerWithBinary(path string, args ...string) *Spawner {
	return &Spawner{binary: path, extraArgs: args, Stderr: os.Stderr}
}

// Spawner launches one claude process per Spawn call and returns a
// [Session] that owns its stdin pipe, stream.Reader, and lifecycle.
// Construct via [NewSpawner].
type Spawner struct {
	binary string
	// extraArgs is prepended to the per-spawn argument list and is only
	// populated by the test-only [newSpawnerWithBinary]: it lets tests
	// thread re-exec sentinels (e.g. -test.run=TestHelperProcess) into
	// the child without disturbing the [buildArgs] shape.
	extraArgs []string

	// Stderr is the writer the spawned process's stderr is connected to.
	// A nil value defaults to [os.Stderr] at Spawn time so callers can
	// leave it unset; tests typically redirect to a [bytes.Buffer].
	Stderr io.Writer
}

// Spawn launches one claude process and returns a [Session]. ctx is
// wired through [exec.CommandContext]: cancelling it triggers SIGINT
// to the whole process group, with SIGKILL escalation after [waitDelay].
func (c *Spawner) Spawn(ctx context.Context, cfg Config) (Session, error) {
	args := append(append([]string(nil), c.extraArgs...), buildArgs(cfg)...)
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Env = buildEnv(cfg)
	cmd.Dir = cfg.WorkDir
	if c.Stderr != nil {
		cmd.Stderr = c.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}

	// Put claude into its own process group so we can signal the entire
	// subtree (claude plus any tool grandchildren). cmd.Cancel sends
	// SIGINT to -pgid, the canonical "kill the whole pipeline" target;
	// WaitDelay then escalates to SIGKILL if the group hasn't exited.
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return signalProcessGroup(cmd.Process.Pid, syscall.SIGINT)
	}
	cmd.WaitDelay = waitDelay

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open agent stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open agent stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start %s: %w", c.binary, err)
	}

	return &claudeSession{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		reader: stream.NewReader(stdout),
	}, nil
}

// claudeSession is the production [Session] implementation; it wraps
// one running claude subprocess.
type claudeSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	reader *stream.Reader

	closeOnce sync.Once
	closeErr  error
}

func (s *claudeSession) Events() *stream.Reader { return s.reader }

func (s *claudeSession) Send(text string) error {
	return stream.WriteUserMessage(s.stdin, text)
}

// Close closes stdin (so claude can exit cleanly), waits for the
// process to reap, and translates the wait outcome into the package's
// public error contract. Subsequent calls return the original result.
//
// To bound the wait, Close drains any remaining stdout in a goroutine
// and arms a [time.AfterFunc] that escalates to a hard kill of the
// process if EOF has not arrived after [closeGrace]. exec.Cmd.Wait
// requires StdoutPipe readers to reach EOF before it can reap the
// child, so a stuck producer would otherwise wedge here forever.
func (s *claudeSession) Close() error {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()

		// Drain stdout to EOF in a goroutine. exec.Cmd.Wait will not
		// return until every reader of a StdoutPipe has reached EOF, so
		// we must consume whatever the child still has buffered before
		// reaping. A misbehaving child could keep us here indefinitely;
		// the AfterFunc below is the escalation backstop.
		drainDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, s.stdout)
			close(drainDone)
		}()

		killer := time.AfterFunc(closeGrace, func() {
			if s.cmd.Process != nil {
				_ = s.cmd.Process.Kill()
			}
		})
		<-drainDone
		killer.Stop()

		s.closeErr = translateWaitErr(s.cmd.Wait())
	})
	return s.closeErr
}

// translateWaitErr maps an [exec.Cmd.Wait] error into the agent
// package's public error vocabulary: nil for a clean exit, *[ExitError]
// for a non-zero exit (or signal death), the original error wrapped
// otherwise.
func translateWaitErr(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		out := &ExitError{Code: ee.ExitCode()}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			out.Signaled = true
			out.Signal = ws.Signal()
		}
		return out
	}
	return err
}

// buildArgs constructs the command-line for a single claude
// invocation.
func buildArgs(cfg Config) []string {
	args := []string{"-p"}
	if cfg.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "--model", cfg.Model, "--effort", cfg.Effort)
	if cfg.Tools != "" {
		args = append(args, "--tools", cfg.Tools)
	}
	args = append(args,
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--replay-user-messages",
		"--json-schema", stream.SchemaJSON,
	)
	return args
}

// buildEnv constructs the environment for the child process. The
// parent's environment is inherited so claude can find PATH, HOME,
// etc.; we layer ralph-specific switches on top.
func buildEnv(cfg Config) []string {
	env := os.Environ()
	if cfg.ConfigDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+cfg.ConfigDir)
	}
	if cfg.OneMContext {
		env = append(env, "CLAUDE_CODE_DISABLE_1M_CONTEXT=0")
	} else {
		env = append(env, "CLAUDE_CODE_DISABLE_1M_CONTEXT=1")
	}
	if cfg.ClaudeAIMCP {
		env = append(env, "ENABLE_CLAUDEAI_MCP_SERVERS=true")
	} else {
		env = append(env, "ENABLE_CLAUDEAI_MCP_SERVERS=false")
	}
	return env
}
