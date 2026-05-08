package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// claudeBinary is the executable name spawned for each iteration.
// Made a var so tests can substitute a fake.
var claudeBinary = "claude"

// maxRetriesPerIteration bounds the number of correction round-trips
// before ralph gives up on the current iteration. Matches the Ruby
// driver.
const maxRetriesPerIteration = 3

// errBadStructuredOutput is returned when a result event's
// structured_output is missing or fails the schema. The outer retry
// loop catches it and re-prompts claude with a correction.
var errBadStructuredOutput = errors.New("invalid structured output")

// errStreamEnded is returned when claude's stdout closes before a
// result event arrives. This is fatal for the iteration; no retry
// can recover it.
var errStreamEnded = errors.New("claude stream ended without result")

// runIteration drives a single claude invocation: it spawns the
// child, sends the kickoff prompt, dispatches events into the
// emitter, and applies the structured-output retry policy. It
// returns the final status ("DONE" or "CONTINUE") on success, or an
// error if the iteration could not complete.
//
// Cancellation of ctx (timeout or SIGINT) sends SIGINT to claude and
// waits for it to wind down before returning ctx.Err().
func runIteration(ctx context.Context, cfg Config, e *render.Emitter, s *stats) (string, error) {
	cmd := exec.CommandContext(ctx, claudeBinary, buildArgs(cfg)...)
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
	cmd.WaitDelay = 10 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("open claude stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("open claude stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", claudeBinary, err)
	}

	r := stream.NewReader(stdout)
	e.ResetIteration()

	status, runErr := pumpStream(ctx, r, stdin, e, s, cfg.Prompt)

	// Always close stdin so claude can exit cleanly, then wait. We
	// surface ctx errors first because they tell the operator whether
	// the run was interrupted vs. naturally completed.
	_ = stdin.Close()
	waitErr := cmd.Wait()

	if cErr := ctx.Err(); cErr != nil {
		return "", cErr
	}
	if runErr != nil {
		return "", runErr
	}
	if waitErr != nil {
		// Narrow the failure-tolerance window to documented cases: claude
		// is known to exit 0 or 1 even when the iteration produced a
		// well-formed result. Anything else (signal death, exit codes
		// >1) bubbles up.
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code := ee.ExitCode()
			if (code == 0 || code == 1) && status != "" {
				return status, nil
			}
			return "", fmt.Errorf("claude exited with status %d: %w", code, waitErr)
		}
		return "", fmt.Errorf("claude exited: %w", waitErr)
	}
	return status, nil
}

// pumpStream sends the kickoff message, then alternates between
// reading events and (on a malformed result) sending corrections, up
// to [maxRetriesPerIteration] times. ctx is honored between retry
// attempts so an operator interrupt isn't held up by a queued
// correction round.
func pumpStream(
	ctx context.Context,
	r *stream.Reader,
	stdin io.Writer,
	e *render.Emitter,
	s *stats,
	prompt string,
) (string, error) {
	if err := writeUserMessage(stdin, prompt); err != nil {
		return "", fmt.Errorf("send kickoff: %w", err)
	}

	for retry := 0; ; retry++ {
		status, err := readUntilResult(r, e, s)
		if err == nil {
			return status, nil
		}
		if !errors.Is(err, errBadStructuredOutput) {
			return "", err
		}
		if retry >= maxRetriesPerIteration {
			return "", fmt.Errorf("%w after %d retries", err, retry)
		}
		// Respect cancellation between attempts. The scanner itself
		// isn't context-aware, but at least the retry loop won't queue
		// another correction once the operator has hit Ctrl-C.
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		if cErr := writeUserMessage(stdin, correctionMessage(err)); cErr != nil {
			return "", fmt.Errorf("send correction: %w", cErr)
		}
	}
}

// readUntilResult drains r until a result event arrives. Each event
// is dispatched into the emitter and tallied in stats. A missing or
// malformed structured_output returns [errBadStructuredOutput] so the
// caller can retry. Unrecognised event types and unparseable lines
// are surfaced verbatim and decoding resumes on the next line, so a
// new event kind from claude does not abort the iteration.
func readUntilResult(r *stream.Reader, e *render.Emitter, s *stats) (string, error) {
	for {
		e.Spinner.Start()
		ev, err := r.Next()
		e.Spinner.Stop()
		if errors.Is(err, io.EOF) {
			return "", errStreamEnded
		}
		if err != nil {
			// Forward-compat and resilience: log the offending line so
			// the operator retains full visibility, then keep reading.
			var de *stream.DecodeError
			if errors.As(err, &de) {
				fmt.Fprintf(os.Stdout, "%s\n\n", de.Bytes)
				continue
			}
			return "", fmt.Errorf("read stream: %w", err)
		}

		s.tallyEvent(ev.Kind())

		switch ev := ev.(type) {
		case stream.Assistant:
			e.OnAssistant(ev)
		case stream.User:
			e.OnUser(ev)
		case stream.Result:
			e.OnResult(ev)
			status := render.DecodeStatus(ev.StructuredOutput)
			if status != stream.StatusDone && status != stream.StatusContinue {
				return "", errBadStructuredOutput
			}
			return status, nil
		case stream.System:
			e.OnSystem(ev)
		case stream.RateLimit:
			e.OnRateLimit(ev)
		case stream.UnknownEvent:
			// Already tallied; the bad-type error path can't reach
			// here because Reader.Next pairs unknowns with an error.
		}
	}
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

// correctionMessage produces the natural-language nudge sent to
// claude after a malformed result event. The text intentionally
// names the schema requirement so the model has the information it
// needs to comply on the next turn.
func correctionMessage(cause error) string {
	return fmt.Sprintf(
		"Your previous reply did not satisfy the required structured output (%v). "+
			"Reply again, this time using the StructuredOutput tool exactly once with "+
			`{"status":"DONE"} or {"status":"CONTINUE"}.`,
		cause,
	)
}
