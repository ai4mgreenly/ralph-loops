package agent

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// argAfter returns the token immediately following the first
// occurrence of flag in args, plus whether flag was found with a value.
func argAfter(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i == len(args)-1 {
		return "", false
	}
	return args[i+1], true
}

func TestBuildArgs_FullConfig(t *testing.T) {
	cfg := Config{
		Prompt:           "do one iteration",
		SystemPromptFile: "/abs/app-root/AGENTS.md",
		Provider:         "anthropic",
		Model:            "anthropic/claude-sonnet-4-6:high",
		Thinking:         "high",
		Tools:            "read,bash",
		WorkDir:          "/abs/app-root",
		Raw:              true,
	}
	got := buildArgs(cfg)

	// The fixed pi one-shot framing.
	mustContain(t, got, "-p")
	mustContain(t, got, "--mode", "json")
	mustContain(t, got, "--no-session")
	mustContain(t, got, "--no-context-files")
	mustContain(t, got, "--append-system-prompt", "/abs/app-root/AGENTS.md")
	mustContain(t, got, "--no-extensions")
	mustContain(t, got, "--no-skills")
	mustContain(t, got, "--no-prompt-templates")
	mustContain(t, got, "--no-themes")

	// Operator-supplied tools replace the default allowlist verbatim.
	if v, ok := argAfter(got, "--tools"); !ok || v != "read,bash" {
		t.Errorf("--tools = %q (ok=%v), want %q: %v", v, ok, "read,bash", got)
	}

	// Optional pass-throughs present because they were set.
	mustContain(t, got, "--provider", "anthropic")
	mustContain(t, got, "--model", "anthropic/claude-sonnet-4-6:high")
	mustContain(t, got, "--thinking", "high")
	mustContain(t, got, "--raw")

	// The kickoff prompt is the trailing positional.
	if got[len(got)-1] != "do one iteration" {
		t.Errorf("last arg = %q, want the kickoff prompt: %v", got[len(got)-1], got)
	}

	// None of the deleted claude/stream-json flags survive.
	for _, dead := range []string{
		"--dangerously-skip-permissions",
		"--effort",
		"--input-format",
		"--output-format",
		"--replay-user-messages",
		"--json-schema",
		"--verbose",
		"--config-dir",
		"--engine",
		"--one-m-context",
		"--claude-ai-mcp",
	} {
		if slices.Contains(got, dead) {
			t.Errorf("dead claude flag %q must not appear: %v", dead, got)
		}
	}
}

func TestBuildArgs_DefaultToolsAllowlist(t *testing.T) {
	// With no operator Tools value, ralph injects the full built-in
	// allowlist so the build agent gets every pi built-in.
	got := buildArgs(Config{Prompt: "go"})
	v, ok := argAfter(got, "--tools")
	if !ok {
		t.Fatalf("--tools missing: %v", got)
	}
	if v != "read,bash,edit,write,grep,find,ls" {
		t.Errorf("default --tools = %q, want %q", v, "read,bash,edit,write,grep,find,ls")
	}
}

func TestBuildArgs_OptionalPassThroughsOmittedWhenUnset(t *testing.T) {
	// Provider/model/thinking have no ralph default: the flag must be
	// absent entirely so pi uses its own settings.json.
	got := buildArgs(Config{Prompt: "go"})
	for _, flag := range []string{"--provider", "--model", "--thinking"} {
		if slices.Contains(got, flag) {
			t.Errorf("%s must be omitted when unset: %v", flag, got)
		}
	}
}

func TestBuildArgs_OmitsRaw(t *testing.T) {
	got := buildArgs(Config{Prompt: "go"})
	if slices.Contains(got, "--raw") {
		t.Errorf("--raw should be omitted when Raw is false: %v", got)
	}
}

func TestBuildArgs_RawForwarded(t *testing.T) {
	got := buildArgs(Config{Prompt: "go", Raw: true})
	if !slices.Contains(got, "--raw") {
		t.Errorf("--raw should be forwarded when Raw is true: %v", got)
	}
}

func TestBuildArgs_OmitsAppendSystemPromptWhenUnset(t *testing.T) {
	// An empty SystemPromptFile means no persona injection flag; pi
	// would fall back to its base prompt (the loop always sets this in
	// production, but buildArgs must not emit a flag with no value).
	got := buildArgs(Config{Prompt: "go"})
	if slices.Contains(got, "--append-system-prompt") {
		t.Errorf("--append-system-prompt should be omitted when SystemPromptFile is empty: %v", got)
	}
}

func TestBuildArgs_PromptIsTrailingPositional(t *testing.T) {
	// pi's -p consumes the next non-flag token as the prompt, so the
	// kickoff must be the very last argv element regardless of which
	// optional flags are present.
	got := buildArgs(Config{Prompt: "KICKOFF", SystemPromptFile: "/x/AGENTS.md", Model: "m"})
	if got[len(got)-1] != "KICKOFF" {
		t.Errorf("prompt must be last arg, got %v", got)
	}
}

func TestTranslateWaitErr_AllBranches(t *testing.T) {
	if err := translateWaitErr(nil); err != nil {
		t.Errorf("nil wait err should translate to nil, got %v", err)
	}

	// A non-*exec.ExitError is returned as-is.
	plain := errors.New("boom")
	if got := translateWaitErr(plain); !errors.Is(got, plain) {
		t.Errorf("plain error should pass through, got %v", got)
	}

	// An exit-code error becomes *ExitError. We can't fabricate an
	// *exec.ExitError directly without running a process, so spawn
	// `false` which exits with code 1.
	cmd := exec.Command("false")
	waitErr := cmd.Run()
	got := translateWaitErr(waitErr)
	var ee *ExitError
	if !errors.As(got, &ee) {
		t.Fatalf("expected *ExitError, got %T (%v)", got, got)
	}
	if ee.Code != 1 {
		t.Errorf("expected code 1 from `false`, got %d", ee.Code)
	}
}

func TestExitError_Message(t *testing.T) {
	err := &ExitError{Code: 42}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("ExitError message should include the code: %q", err.Error())
	}

	sig := &ExitError{Code: 143, Signaled: true, Signal: 15}
	if !strings.Contains(sig.Error(), "signal") {
		t.Errorf("signalled ExitError message should mention the signal: %q", sig.Error())
	}
}

func mustContain(t *testing.T, args []string, want ...string) {
	t.Helper()
	for i := 0; i < len(args); i++ {
		if args[i] == want[0] {
			if len(want) == 1 {
				return
			}
			if i+1 < len(args) && args[i+1] == want[1] {
				return
			}
		}
	}
	t.Errorf("expected %v in args %v", want, args)
}
