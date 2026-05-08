package agent

import (
	"context"
	"encoding/json"
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
// Tests substitute by constructing claudeSpawner directly.
const claudeBinary = "claude"

// waitDelay caps how long [exec.Cmd.Wait] will block after the
// context cancels and SIGINT has been delivered. After this elapses
// the runtime escalates to SIGKILL on the process group.
const waitDelay = 10 * time.Second

// NewClaude returns the production [Spawner] that runs the `claude`
// CLI from $PATH. Each Spawn invokes the binary anew; nothing is
// cached between iterations.
func NewClaude() Spawner {
	return &claudeSpawner{binary: claudeBinary}
}

type claudeSpawner struct {
	binary string
}

// Spawn launches one claude process and returns a [Session] that
// owns its stdin pipe, stream.Reader, and lifecycle. ctx is wired
// through [exec.CommandContext]: cancelling it triggers SIGINT to the
// whole process group.
func (s *claudeSpawner) Spawn(ctx context.Context, cfg Config) (Session, error) {
	cmd := exec.CommandContext(ctx, s.binary, buildArgs(cfg)...)
	cmd.Env = buildEnv(cfg)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = os.Stderr

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
		return nil, fmt.Errorf("start %s: %w", s.binary, err)
	}

	return &claudeSession{
		cmd:    cmd,
		stdin:  stdin,
		reader: stream.NewReader(stdout),
	}, nil
}

type claudeSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *stream.Reader

	closeOnce sync.Once
	closeErr  error
}

func (s *claudeSession) Events() *stream.Reader { return s.reader }

func (s *claudeSession) Send(text string) error {
	return writeUserMessage(s.stdin, text)
}

// Close closes stdin (so claude can exit cleanly), waits for the
// process to reap, and translates the wait outcome into the package's
// public error contract. Subsequent calls return the original result.
func (s *claudeSession) Close() error {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		s.closeErr = translateWaitErr(s.cmd.Wait())
	})
	return s.closeErr
}

// translateWaitErr maps an [exec.Cmd.Wait] error into the agent
// package's public error vocabulary: nil for a clean exit, *[ExitError]
// for a non-zero exit, the original error wrapped otherwise (signal
// death, runtime issues).
func translateWaitErr(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode()}
	}
	return err
}

// userMessage is the wire-format envelope for a single stream-json
// user message line written to claude's stdin.
type userMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// writeUserMessage writes a single stream-json user message line to
// w, terminated with a newline as required by the protocol.
func writeUserMessage(w io.Writer, text string) error {
	var msg userMessage
	msg.Type = "user"
	msg.Message.Role = "user"
	msg.Message.Content = []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: text}}

	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal user message: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("write user message: %w", err)
	}
	return nil
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

// setProcessGroup arranges for cmd to run in its own process group so
// signals delivered to -pgid reach the entire subtree (claude plus any
// tool grandchildren). Unix-only; the syscall.SysProcAttr.Setpgid
// field is not portable to Windows, but ralph is Unix-only.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalProcessGroup sends sig to the process group whose leader has
// the given pid. Negating the pid is the syscall.Kill convention for
// "deliver to every member of the group."
func signalProcessGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
