package loop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

func TestStats_TrackUsageComputesCost(t *testing.T) {
	s := newStats("opus")
	s.trackUsage(&stream.Usage{
		InputTokens:              1_000_000,
		OutputTokens:             1_000_000,
		CacheReadInputTokens:     1_000_000,
		CacheCreationInputTokens: 1_000_000,
	})
	// Opus: 5 + 25 + 0.5 + 6.25 = 36.75 USD per million of each.
	if s.cost < 36.749 || s.cost > 36.751 {
		t.Errorf("cost = %.4f, want ~36.75", s.cost)
	}
	if got, want := s.tokens.total(), 4_000_000; got != want {
		t.Errorf("tokens.total = %d, want %d", got, want)
	}
}

func TestStats_TrackUsageUnknownModelStillTalliesTokens(t *testing.T) {
	s := newStats("nonexistent")
	s.trackUsage(&stream.Usage{InputTokens: 100})
	if s.cost != 0 {
		t.Errorf("expected zero cost for unknown model, got %f", s.cost)
	}
	if s.tokens.input != 100 {
		t.Errorf("expected token count to be tallied even without pricing")
	}
}

func TestStats_PanelLayout(t *testing.T) {
	s := newStats("opus")
	s.iterations = 2
	s.tallyEvent(stream.TypeAssistant)
	s.tallyEvent(stream.TypeAssistant)
	s.tallyEvent(stream.TypeResult)
	s.tallyEvent("custom_kind") // unknown -> appears below the known set
	s.tallyBlock(stream.BlockText)
	s.tallyBlock(stream.BlockToolUse)
	s.trackUsage(&stream.Usage{InputTokens: 12_345})
	s.addLLMTime(2 * time.Second)
	s.addToolTime(time.Second)

	var buf bytes.Buffer
	s.writePanel(&buf, "/some/reqs", "done")
	out := buf.String()

	wantSubstrings := []string{
		statsRuleChar + statsRuleChar + statsRuleChar, // unicode rule appears
		"reqs:        /some/reqs",
		"exit:        done",
		"iterations:  2",
		"assistant          2",
		"result             1",
		"custom_kind        1",
		"text               1",
		"tool_use           1",
		"input:        12,345",
		"cost:        $",
		"  llm:    2s",
		"  tools:  1s",
		"  start:  ",
		"  end:    ",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(out, sub) {
			t.Errorf("panel missing %q\n--- panel ---\n%s", sub, out)
		}
	}
	// Old ASCII rule should be gone.
	if strings.Contains(out, "====") {
		t.Errorf("panel still uses the old ASCII rule:\n%s", out)
	}
}

func TestStats_PanelOmitsExitWhenEmpty(t *testing.T) {
	s := newStats("opus")
	var buf bytes.Buffer
	s.writePanel(&buf, "/some/reqs", "")
	if strings.Contains(buf.String(), "exit:") {
		t.Errorf("expected no exit line, got panel:\n%s", buf.String())
	}
}

func TestAppendResultsJSONL_CreatesDirAndAppends(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".ralph-loops")
	prev := resultsHomePath
	resultsHomePath = func() string { return dir }
	defer func() { resultsHomePath = prev }()

	first := summary{Reqs: "/r", Exit: "done", Iterations: 1}
	second := summary{Reqs: "/r", Exit: "timeout", Iterations: 7}
	appendResultsJSONL(first)
	appendResultsJSONL(second)

	logPath := filepath.Join(dir, "results.jsonl")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), body)
	}

	var got summary
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatalf("unmarshal second line: %v", err)
	}
	if got.Iterations != 7 || got.Exit != "timeout" {
		t.Errorf("second record = %+v", got)
	}
}

func TestAppendResultsJSONL_SilentWhenHomeUnknown(t *testing.T) {
	prev := resultsHomePath
	resultsHomePath = func() string { return "" }
	defer func() { resultsHomePath = prev }()

	// Should not panic or otherwise misbehave.
	appendResultsJSONL(summary{Reqs: "/r"})
}

func TestAppendResultsJSONL_SilentWhenMkdirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only directory check is unreliable")
	}
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "ro")
	if err := os.MkdirAll(parent, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prev := resultsHomePath
	resultsHomePath = func() string { return filepath.Join(parent, "child") }
	defer func() { resultsHomePath = prev }()

	appendResultsJSONL(summary{Reqs: "/r"})

	// The child directory must NOT have been created — our refusal to
	// write means we also didn't fall back to creating it elsewhere.
	if _, err := os.Stat(filepath.Join(parent, "child")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected child dir to be absent, stat err = %v", err)
	}
}

func TestStats_SnapshotShape(t *testing.T) {
	s := newStats("opus")
	s.iterations = 3
	s.tallyEvent(stream.TypeAssistant)
	s.tallyBlock(stream.BlockText)
	s.trackUsage(&stream.Usage{InputTokens: 100, OutputTokens: 50})
	s.addLLMTime(5 * time.Second)
	s.addToolTime(2 * time.Second)

	sum := s.snapshot("/path/to/reqs", "done")
	if sum.Reqs != "/path/to/reqs" {
		t.Errorf("Reqs = %q", sum.Reqs)
	}
	if sum.Exit != "done" {
		t.Errorf("Exit = %q", sum.Exit)
	}
	if sum.Iterations != 3 {
		t.Errorf("Iterations = %d", sum.Iterations)
	}
	if sum.Tokens.Input != 100 || sum.Tokens.Output != 50 || sum.Tokens.Total != 150 {
		t.Errorf("Tokens = %+v", sum.Tokens)
	}
	if sum.Time.LLMSeconds != 5 || sum.Time.ToolsSeconds != 2 {
		t.Errorf("Time = %+v", sum.Time)
	}
	if sum.Time.Start.IsZero() {
		t.Error("Time.Start should be populated from stats.startTime")
	}
	if sum.Time.End.Before(sum.Time.Start) {
		t.Errorf("Time.End (%v) should not precede Time.Start (%v)", sum.Time.End, sum.Time.Start)
	}
	// Snapshot must clone the maps so later tallies don't mutate the
	// frozen record.
	s.tallyEvent(stream.TypeAssistant)
	if sum.Events[stream.TypeAssistant] != 1 {
		t.Errorf("snapshot Events should be cloned; got %v", sum.Events)
	}
}
