package loop

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
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

	// json-schema must contain the status enum.
	idx := slices.Index(got, "--json-schema")
	if idx < 0 || idx == len(got)-1 {
		t.Fatalf("--json-schema flag missing or has no value: %v", got)
	}
	if !strings.Contains(got[idx+1], `"DONE"`) || !strings.Contains(got[idx+1], `"CONTINUE"`) {
		t.Errorf("schema missing status enum: %q", got[idx+1])
	}
}

func TestBuildArgsOmitsTools(t *testing.T) {
	cfg := Config{Model: "opus", Effort: "medium"}
	got := buildArgs(cfg)
	if slices.Contains(got, "--tools") {
		t.Errorf("--tools should be omitted when Tools is empty: %v", got)
	}
}

func TestBuildArgsOmitsSkipPermissions(t *testing.T) {
	cfg := Config{Model: "opus", Effort: "medium", SkipPermissions: false}
	got := buildArgs(cfg)
	if slices.Contains(got, "--dangerously-skip-permissions") {
		t.Errorf("--dangerously-skip-permissions should be omitted when SkipPermissions is false: %v", got)
	}
}

func TestBuildEnv(t *testing.T) {
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

func TestBuildEnvOmitsConfigDirWhenEmpty(t *testing.T) {
	env := buildEnv(Config{OneMContext: true})
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("CLAUDE_CONFIG_DIR should not be set when ConfigDir is empty: %q", e)
		}
	}
}

func TestBuildEnvDisablesOneMWhenOff(t *testing.T) {
	env := buildEnv(Config{OneMContext: false})
	if !envContains(env, "CLAUDE_CODE_DISABLE_1M_CONTEXT=1") {
		t.Errorf("expected CLAUDE_CODE_DISABLE_1M_CONTEXT=1 when OneMContext is false")
	}
}

func TestWriteUserMessageFraming(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUserMessage(&buf, "hello"); err != nil {
		t.Fatalf("writeUserMessage: %v", err)
	}

	out := buf.Bytes()
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Fatalf("user message must end with newline, got %q", out)
	}

	var decoded userMessage
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &decoded); err != nil {
		t.Fatalf("unmarshal written line: %v", err)
	}
	if decoded.Type != "user" || decoded.Message.Role != "user" {
		t.Errorf("envelope roles wrong: %+v", decoded)
	}
	if len(decoded.Message.Content) != 1 || decoded.Message.Content[0].Text != "hello" {
		t.Errorf("content not preserved: %+v", decoded.Message.Content)
	}
}

func TestCorrectionMessageMentionsSchema(t *testing.T) {
	got := correctionMessage(errBadStructuredOutput)
	for _, sub := range []string{"DONE", "CONTINUE", "structured"} {
		if !strings.Contains(got, sub) {
			t.Errorf("correction message missing %q: %q", sub, got)
		}
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
