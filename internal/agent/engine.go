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

// waitDelay caps how long [exec.Cmd.Wait] will block after the context
// cancels and SIGTERM has been delivered to the process group. After
// this elapses the runtime escalates to SIGKILL on the group. pi
// print-mode handles SIGTERM cleanly (exit 143); the grace window lets
// it flush its final events before the hard kill.
const waitDelay = 10 * time.Second

// closeGrace is the upper bound [Session.Close] waits for the
// stdout-drain goroutine to reach EOF before escalating to SIGKILL on
// the process. It exists so a child stuck mid-write cannot wedge the
// loop forever; five seconds is comfortably longer than any tool
// response observed in practice.
const closeGrace = 5 * time.Second

// defaultTools is ralph's default `--tools` allowlist for the build
// agent: the full set of pi's seven built-in tools. pi's own default
// enables only read/write/edit/bash (grep/find/ls are read-only and
// off by default); ralph opts the build agent into every built-in
// because the search tools make codebase navigation effective and
// carry no determinism cost — built-ins are part of the controlled
// environment, unlike extensions/skills. An operator-supplied
// Config.Tools replaces this verbatim.
const defaultTools = "read,bash,edit,write,grep,find,ls"

// NewSpawner returns the production [*Spawner] that runs the named pi
// binary from $PATH. Each Spawn invokes the binary anew in one-shot
// `pi -p --mode json` print mode; nothing is cached between iterations.
func NewSpawner(binary string) *Spawner {
	return &Spawner{binary: binary, Stderr: os.Stderr}
}

// newSpawnerWithExtraArgs is the test seam used by integration tests in
// this package: it returns a [*Spawner] that runs the named binary with
// extraArgs prepended to the per-spawn argument list. Tests use it to
// thread re-exec sentinels (e.g. -test.run=TestHelperProcess) into the
// child without disturbing the [buildArgs] shape. Production code
// constructs spawners via [NewSpawner] and never sees this constructor.
func newSpawnerWithExtraArgs(binary string, args ...string) *Spawner {
	return &Spawner{binary: binary, extraArgs: args, Stderr: os.Stderr}
}

// Spawner launches one pi process per Spawn call and returns a
// [Session] that owns its stream.Reader and lifecycle. Construct via
// [NewSpawner].
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

	// Stdout, if non-nil, taps pi's stdout: every byte read off the pipe
	// is copied verbatim to this writer before being handed to the
	// [stream.Reader]. Used by ralph's --raw mode to dump pi's JSONL
	// wire output untouched. A nil value (the default) leaves the pipe
	// untapped.
	Stdout io.Writer
}

// Spawn launches one pi process and returns a [Session]. ctx is wired
// through [exec.CommandContext]: cancelling it triggers SIGTERM to the
// whole process group, with SIGKILL escalation after [waitDelay].
//
// The child's stdin is /dev/null (immediate EOF). This is mandatory:
// pi's print mode reads piped stdin to EOF before starting, so a
// never-closed stdin pipe makes pi block forever (confirmed in
// practice). pi has no stdin user-message protocol — the kickoff is the
// trailing positional argument instead.
func (c *Spawner) Spawn(ctx context.Context, cfg Config) (Session, error) {
	args := append(append([]string(nil), c.extraArgs...), buildArgs(cfg)...)
	cmd := exec.CommandContext(ctx, c.binary, args...)
	// The parent environment is inherited (PATH, HOME so pi finds
	// ~/.pi/agent/auth.json, etc.). ralph layers no env switches: pi
	// owns its own configuration and there is no CLAUDE_CONFIG_DIR
	// analog to relocate.
	cmd.Env = os.Environ()
	cmd.Dir = cfg.WorkDir
	if c.Stderr != nil {
		cmd.Stderr = c.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}

	// Child stdin = /dev/null: pi's readStdin() reads piped stdin to EOF
	// and a never-closed pipe blocks pi forever (probe-confirmed). An
	// /dev/null fd yields immediate EOF, which is exactly what one-shot
	// print mode needs.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, fmt.Errorf("open %s for agent stdin: %w", os.DevNull, err)
	}
	cmd.Stdin = devNull

	// Put pi into its own process group so we can signal the entire
	// subtree (pi plus any tool grandchildren). cmd.Cancel sends SIGTERM
	// to -pgid: pi print-mode installs a SIGTERM handler and exits
	// cleanly (143), whereas Go's exec.CommandContext default is SIGKILL
	// and pi installs no SIGINT handler. WaitDelay then escalates to
	// SIGKILL on the group if it has not exited within the grace window.
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return signalProcessGroup(cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = waitDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = devNull.Close()
		return nil, fmt.Errorf("open agent stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start %s: %w", c.binary, err)
	}
	// The child holds its own dup of the fd now; the parent's copy is
	// no longer needed.
	_ = devNull.Close()

	var src io.Reader = stdout
	if c.Stdout != nil {
		src = io.TeeReader(stdout, c.Stdout)
	}

	return &engineSession{
		cmd:    cmd,
		stdout: stdout,
		src:    src,
		reader: stream.NewReader(src),
	}, nil
}

// engineSession is the production [Session] implementation; it wraps
// one running pi subprocess.
type engineSession struct {
	cmd    *exec.Cmd
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

// Send is a no-op in the production pi Session. pi runs one-shot in
// print mode with the kickoff already delivered as the argv positional
// at spawn; the process is exiting and has no stdin user-message
// protocol, so there is nothing to inject. The method exists solely so
// the production type satisfies the same [Session] interface the loop's
// fakes implement (those fakes drive canned JSONL through Send). It
// returns nil so existing call sites that still invoke Send remain
// harmless during the migration.
func (s *engineSession) Send(text string) error { return nil }

// Close waits for the process to reap and translates the wait outcome
// into the package's public error contract. Subsequent calls return
// the original result. Unlike the old claude session there is no stdin
// pipe to close — pi's stdin is /dev/null and the prompt was already
// passed as argv — so Close's only job is to drain stdout and reap.
//
// To bound the wait, Close drains any remaining stdout in a goroutine
// and arms a [time.AfterFunc] that escalates to a hard kill of the
// process if EOF has not arrived after [closeGrace]. exec.Cmd.Wait
// requires StdoutPipe readers to reach EOF before it can reap the
// child, so a stuck producer would otherwise wedge here forever.
func (s *engineSession) Close() error {
	s.closeOnce.Do(func() {
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

// buildArgs constructs the command-line for a single one-shot pi
// invocation. The shape is fixed by the locked pi-migration record:
//
//	pi -p --mode json --no-session --no-context-files
//	   --append-system-prompt <abs AGENTS.md>
//	   --no-extensions --no-skills --no-prompt-templates --no-themes
//	   --tools <allowlist>
//	   [--provider X] [--model Y] [--thinking Z] [--raw]
//	   <kickoff prompt>
//
// Rationale per flag:
//   - `-p --mode json`     one-shot print mode, native JSONL stream.
//   - `--no-session`       ephemeral; ralph never resumes.
//   - `--no-context-files` suppresses ALL AGENTS.md/CLAUDE.md discovery
//     (the persona is injected explicitly instead).
//   - `--append-system-prompt <file>` injects the build-agent persona.
//   - `--no-extensions --no-skills --no-prompt-templates --no-themes`
//     suppress ambient injection that could add tools/flags/system
//     text or block a headless loop.
//   - `--tools`            explicit built-in allowlist (default = all
//     seven; operator value overrides).
//   - `--provider/--model/--thinking` optional pass-throughs, omitted
//     unless the operator set them (pi uses its own defaults).
//   - `--raw`              engine-neutral operator debug passthrough.
//
// The kickoff prompt is the trailing positional: pi's `-p` consumes the
// next non-flag token as the prompt, so it must come last and after a
// flag-terminating boundary is unnecessary (it is a value, not a flag).
func buildArgs(cfg Config) []string {
	args := []string{
		"-p",
		"--mode", "json",
		"--no-session",
		"--no-context-files",
	}
	if cfg.SystemPromptFile != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPromptFile)
	}
	args = append(args,
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
	)

	tools := cfg.Tools
	if tools == "" {
		tools = defaultTools
	}
	args = append(args, "--tools", tools)

	// Optional pass-throughs: ralph has no defaults for these, so the
	// flag is omitted entirely when unset and pi falls back to its own
	// ~/.pi/agent/settings.json.
	if cfg.Provider != "" {
		args = append(args, "--provider", cfg.Provider)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.Thinking != "" {
		args = append(args, "--thinking", cfg.Thinking)
	}
	if cfg.Raw {
		args = append(args, "--raw")
	}

	// Kickoff prompt last: pi's -p consumes the next non-flag token as
	// the prompt.
	args = append(args, cfg.Prompt)
	return args
}
