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

// NewSpawner returns the production [*Spawner] that runs the named
// engine binary from $PATH. The engine must implement claude's
// stream-json wire contract; the canonical implementation is the
// `claude` CLI, but any drop-in replacement works. Each Spawn invokes
// the binary anew; nothing is cached between iterations.
func NewSpawner(binary string) *Spawner {
	return &Spawner{binary: binary, Stderr: os.Stderr}
}

// newSpawnerWithExtraArgs is the test seam used by integration tests
// in this package: it returns a [*Spawner] that runs the named binary
// with extraArgs prepended to the per-spawn argument list. Tests use
// it to thread re-exec sentinels (e.g. -test.run=TestHelperProcess)
// into the child without disturbing the [buildArgs] shape. Production
// code constructs spawners via [NewSpawner] and never sees this
// constructor.
func newSpawnerWithExtraArgs(binary string, args ...string) *Spawner {
	return &Spawner{binary: binary, extraArgs: args, Stderr: os.Stderr}
}

// Spawner launches one engine process per Spawn call and returns a
// [Session] that owns its stdin pipe, stream.Reader, and lifecycle.
// Construct via [NewSpawner].
type Spawner struct {
	binary string
	// extraArgs is prepended to the per-spawn argument list and is only
	// populated by the test-only [newSpawnerWithExtraArgs]: it lets
	// tests thread re-exec sentinels (e.g. -test.run=TestHelperProcess)
	// into the child without disturbing the [buildArgs] shape.
	extraArgs []string

	// Stderr is the writer the spawned process's stderr is connected to.
	// A nil value defaults to [os.Stderr] at Spawn time so callers can
	// leave it unset; tests typically redirect to a [bytes.Buffer].
	Stderr io.Writer

	// Stdout, if non-nil, taps the engine's stdout: every byte read off
	// the pipe is copied verbatim to this writer before being handed to
	// the [stream.Reader]. Used by ralph's --raw mode to dump the
	// engine's wire output untouched. A nil value (the default) leaves
	// the pipe untapped.
	Stdout io.Writer
}

// Spawn launches one engine process and returns a [Session]. ctx is
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

	// Put the engine into its own process group so we can signal the
	// entire subtree (engine plus any tool grandchildren). cmd.Cancel
	// sends SIGINT to -pgid, the canonical "kill the whole pipeline"
	// target; WaitDelay then escalates to SIGKILL if the group hasn't
	// exited.
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

	var src io.Reader = stdout
	if c.Stdout != nil {
		src = io.TeeReader(stdout, c.Stdout)
	}

	return &engineSession{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		src:    src,
		reader: stream.NewReader(src),
	}, nil
}

// engineSession is the production [Session] implementation; it wraps
// one running engine subprocess.
type engineSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	// src is what stream.Reader (and Close's drain) consume from. It
	// equals stdout in the untapped case, or io.TeeReader(stdout, tap)
	// when Stdout was set on the Spawner. Draining via src — not
	// stdout — keeps the tap whole at close time, when the scanner's
	// buffer plus any unread pipe bytes still need to flow through.
	src    io.Reader
	reader *stream.Reader

	closeOnce sync.Once
	closeErr  error
}

func (s *engineSession) Events() *stream.Reader { return s.reader }

func (s *engineSession) Send(text string) error {
	return stream.WriteUserMessage(s.stdin, text)
}

// Close closes stdin (so the engine can exit cleanly), waits for the
// process to reap, and translates the wait outcome into the package's
// public error contract. Subsequent calls return the original result.
//
// To bound the wait, Close drains any remaining stdout in a goroutine
// and arms a [time.AfterFunc] that escalates to a hard kill of the
// process if EOF has not arrived after [closeGrace]. exec.Cmd.Wait
// requires StdoutPipe readers to reach EOF before it can reap the
// child, so a stuck producer would otherwise wedge here forever.
func (s *engineSession) Close() error {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()

		// Drain stdout to EOF in a goroutine. exec.Cmd.Wait will not
		// return until every reader of a StdoutPipe has reached EOF, so
		// we must consume whatever the child still has buffered before
		// reaping. A misbehaving child could keep us here indefinitely;
		// the AfterFunc below is the escalation backstop.
		drainDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, s.src)
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

// buildArgs constructs the command-line for a single engine
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
	if cfg.Raw {
		args = append(args, "--raw")
	}
	return args
}

// buildEnv constructs the environment for the child process. The
// parent's environment is inherited so the engine can find PATH,
// HOME, etc.; we layer ralph-specific switches on top.
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
