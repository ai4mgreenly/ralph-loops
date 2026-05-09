package agent

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestBuildArgs_FullConfig(t *testing.T) {
	cfg := Config{
		Model:           "opus",
		Effort:          "medium",
		Tools:           "Bash,Read",
		SkipPermissions: true,
	}
	got := buildArgs(cfg)

	mustContain(t, got, "-p")
	mustContain(t, got, "--dangerously-skip-permissions")
	mustContain(t, got, "--model", "opus")
	mustContain(t, got, "--effort", "medium")
	mustContain(t, got, "--tools", "Bash,Read")
	mustContain(t, got, "--input-format", "stream-json")
	mustContain(t, got, "--output-format", "stream-json")
	mustContain(t, got, "--replay-user-messages")

	idx := slices.Index(got, "--json-schema")
	if idx < 0 || idx == len(got)-1 {
		t.Fatalf("--json-schema flag missing or has no value: %v", got)
	}
	if !strings.Contains(got[idx+1], `"DONE"`) || !strings.Contains(got[idx+1], `"CONTINUE"`) {
		t.Errorf("schema missing status enum: %q", got[idx+1])
	}
}

func TestBuildArgs_OmitsTools(t *testing.T) {
	cfg := Config{Model: "opus", Effort: "medium"}
	got := buildArgs(cfg)
	if slices.Contains(got, "--tools") {
		t.Errorf("--tools should be omitted when Tools is empty: %v", got)
	}
}

func TestBuildArgs_OmitsSkipPermissions(t *testing.T) {
	cfg := Config{Model: "opus", Effort: "medium", SkipPermissions: false}
	got := buildArgs(cfg)
	if slices.Contains(got, "--dangerously-skip-permissions") {
		t.Errorf("--dangerously-skip-permissions should be omitted when SkipPermissions is false: %v", got)
	}
}

func TestBuildEnv_FullConfig(t *testing.T) {
	cfg := Config{
		ConfigDir:   "/tmp/cfg",
		OneMContext: true,
		ClaudeAIMCP: false,
	}
	env := buildEnv(cfg)

	wantPairs := map[string]string{
		"CLAUDE_CONFIG_DIR":              "/tmp/cfg",
		"CLAUDE_CODE_DISABLE_1M_CONTEXT": "0",
		"ENABLE_CLAUDEAI_MCP_SERVERS":    "false",
	}
	for k, want := range wantPairs {
		if !envContains(env, k+"="+want) {
			t.Errorf("env missing %s=%s\n%v", k, want, env)
		}
	}
}

func TestBuildEnv_OmitsConfigDirWhenEmpty(t *testing.T) {
	env := buildEnv(Config{OneMContext: true})
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("CLAUDE_CONFIG_DIR should not be set when ConfigDir is empty: %q", e)
		}
	}
}

func TestBuildEnv_DisablesOneMWhenOff(t *testing.T) {
	env := buildEnv(Config{OneMContext: false})
	if !envContains(env, "CLAUDE_CODE_DISABLE_1M_CONTEXT=1") {
		t.Errorf("expected CLAUDE_CODE_DISABLE_1M_CONTEXT=1 when OneMContext is false")
	}
}

func TestTranslateWaitErr_AllBranches(t *testing.T) {
	if err := translateWaitErr(nil); err != nil {
		t.Errorf("nil wait err should translate to nil, got %v", err)
	}

	// A signal-death style error (not *exec.ExitError) is returned as-is.
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

func envContains(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}
