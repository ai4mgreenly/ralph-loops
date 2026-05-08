package loop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// claudeBinary is the executable name spawned for each iteration.
// Made a var so tests can substitute a fake.
var claudeBinary = "claude"

// maxRetriesPerIteration bounds the number of correction round-trips
// before ralph gives up on the current iteration. Matches the Ruby
// driver.
const maxRetriesPerIteration = 3

// scannerInitialBuffer / scannerMaxBuffer size the bufio.Scanner used
// to read claude's stream-json output. Individual events can be quite
// large (a tool result with a long Read or Bash payload), so the
// upper bound is generous.
const (
	scannerInitialBuffer = 64 * 1024
	scannerMaxBuffer     = 16 * 1024 * 1024
)

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
func runIteration(ctx context.Context, cfg Config, e *emitter, s *stats) (string, error) {
	cmd := exec.CommandContext(ctx, claudeBinary, buildArgs(cfg)...)
	cmd.Env = buildEnv(cfg)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
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

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, scannerInitialBuffer), scannerMaxBuffer)
	e.resetIteration()

	status, runErr := pumpStream(scanner, stdin, e, s, cfg.Prompt)

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
		// Non-zero exits after a clean status are tolerated; claude
		// sometimes exits 1 even when the iteration succeeded.
		if status == "" {
			return "", fmt.Errorf("claude exited: %w", waitErr)
		}
	}
	return status, nil
}

// pumpStream sends the kickoff message, then alternates between
// reading events and (on a malformed result) sending corrections, up
// to [maxRetriesPerIteration] times.
func pumpStream(
	scanner *bufio.Scanner,
	stdin io.Writer,
	e *emitter,
	s *stats,
	prompt string,
) (string, error) {
	if err := writeUserMessage(stdin, prompt); err != nil {
		return "", fmt.Errorf("send kickoff: %w", err)
	}

	for retry := 0; ; retry++ {
		status, err := readUntilResult(scanner, e, s)
		if err == nil {
			return status, nil
		}
		if !errors.Is(err, errBadStructuredOutput) {
			return "", err
		}
		if retry >= maxRetriesPerIteration {
			return "", fmt.Errorf("%w after %d retries", err, retry)
		}
		if cErr := writeUserMessage(stdin, correctionMessage(err)); cErr != nil {
			return "", fmt.Errorf("send correction: %w", cErr)
		}
	}
}

// readUntilResult drains the scanner until a result event arrives.
// Each event is dispatched into the emitter and tallied in stats. A
// missing or malformed structured_output returns
// [errBadStructuredOutput] so the caller can retry.
func readUntilResult(scanner *bufio.Scanner, e *emitter, s *stats) (string, error) {
	for {
		e.spinner.Start()
		ok := scanner.Scan()
		e.spinner.Stop()
		if !ok {
			break
		}
		line := scanner.Bytes()
		var raw stream.RawEvent
		if err := json.Unmarshal(line, &raw); err != nil {
			// Pass unparseable lines through verbatim so the operator
			// retains full visibility into what claude is emitting.
			fmt.Fprintf(os.Stdout, "%s\n\n", line)
			continue
		}

		s.tallyEvent(raw.Type)

		switch raw.Type {
		case stream.TypeAssistant:
			var ev stream.Assistant
			if err := json.Unmarshal(raw.Payload, &ev); err == nil {
				e.onAssistant(ev)
			}
		case stream.TypeUser:
			var ev stream.User
			if err := json.Unmarshal(raw.Payload, &ev); err == nil {
				e.onUser(ev)
			}
		case stream.TypeResult:
			var ev stream.Result
			if err := json.Unmarshal(raw.Payload, &ev); err != nil {
				return "", fmt.Errorf("decode result event: %w", err)
			}
			e.onResult(ev)
			status := decodeStatus(ev.StructuredOutput)
			if status != stream.StatusDone && status != stream.StatusContinue {
				return "", errBadStructuredOutput
			}
			return status, nil
		case stream.TypeSystem:
			var ev stream.System
			if err := json.Unmarshal(raw.Payload, &ev); err == nil {
				e.onSystem(ev)
			}
		case stream.TypeRateLimit:
			var ev stream.RateLimit
			if err := json.Unmarshal(raw.Payload, &ev); err == nil {
				e.onRateLimit(ev)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read stream: %w", err)
	}
	return "", errStreamEnded
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
