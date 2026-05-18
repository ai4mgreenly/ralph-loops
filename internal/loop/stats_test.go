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
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// pinnedClock is a deterministic time source for snapshot tests.
func pinnedClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// assistantMsg builds a minimal assistant PiMessage carrying one usage.
func assistantMsg(provider, model string, u *stream.Usage) stream.PiMessage {
	return stream.PiMessage{
		Role:     stream.RoleAssistant,
		Provider: provider,
		Model:    model,
		Usage:    u,
		Content:  []stream.ContentBlock{{Type: stream.BlockText, Text: "hi"}},
	}
}

// TestStats_AgentEndIsAuthoritative confirms Q6: the per-iteration
// token/cost figure is the SUM over the assistant messages of an
// agent_end (per-turn, not cumulative), and pi's fractional-USD cost is
// used verbatim — there is no pricing table.
func TestStats_AgentEndIsAuthoritative(t *testing.T) {
	s := newStats("gpt-5.3-codex", pinnedClock(), "")
	s.tallyAgentEnd(stream.AgentEnd{Messages: []stream.PiMessage{
		assistantMsg("openai-codex", "gpt-5.3-codex", &stream.Usage{
			Input: 100, Output: 20, CacheRead: 5, CacheWrite: 1, TotalTokens: 126,
			Cost: stream.Cost{Input: 0.1, Output: 0.2, Total: 0.3},
		}),
		// A user message must not contribute to the tally.
		{Role: stream.RoleUser, Content: []stream.ContentBlock{{Type: stream.BlockText, Text: "x"}}},
		assistantMsg("openai-codex", "gpt-5.3-codex", &stream.Usage{
			Input: 50, Output: 10, TotalTokens: 60,
			Cost: stream.Cost{Total: 0.05},
		}),
	}})

	sum := s.snapshot("/r", exitDone)
	if sum.Partial {
		t.Error("summary must not be partial once agent_end is folded in")
	}
	if got, want := sum.Tokens.Input, 150; got != want {
		t.Errorf("input = %d, want %d", got, want)
	}
	if got, want := sum.Tokens.Total, 186; got != want {
		t.Errorf("total = %d, want %d", got, want)
	}
	if got, want := sum.Cost, 0.35; got != want {
		t.Errorf("cost = %v, want %v (pi's authoritative fractional USD)", got, want)
	}
	if len(sum.ByModel) != 1 {
		t.Fatalf("by-model rows = %d, want 1: %+v", len(sum.ByModel), sum.ByModel)
	}
	row := sum.ByModel[0]
	if row.Provider != "openai-codex" || row.Model != "gpt-5.3-codex" {
		t.Errorf("by-model key = %+v", row)
	}
	if row.Cost != 0.35 || row.Tokens.Total != 186 {
		t.Errorf("by-model tally = %+v", row)
	}
}

// TestStats_EffectiveModelGroups confirms the Q6 effective-model rule:
// responseModel wins over model when present.
func TestStats_EffectiveModelGroups(t *testing.T) {
	s := newStats("req-model", pinnedClock(), "")
	m := assistantMsg("prov", "req-model", &stream.Usage{Input: 1, TotalTokens: 1, Cost: stream.Cost{Total: 0.01}})
	m.ResponseModel = "served-model"
	s.tallyAgentEnd(stream.AgentEnd{Messages: []stream.PiMessage{m}})

	sum := s.snapshot("/r", exitDone)
	if len(sum.ByModel) != 1 || sum.ByModel[0].Model != "served-model" {
		t.Errorf("expected effective model 'served-model', got %+v", sum.ByModel)
	}
}

// TestStats_PartialFallback confirms Q6: with no agent_end the run
// total falls back to the partial sum of assistant message_end usages,
// and the summary is flagged Partial so a consumer can tell.
func TestStats_PartialFallback(t *testing.T) {
	s := newStats("m", pinnedClock(), "")
	s.TrackMessageUsage(&stream.Usage{Input: 7, Output: 3, TotalTokens: 10, Cost: stream.Cost{Total: 0.02}}, "p", "m", "stop")
	s.TrackMessageUsage(nil, "p", "m", "stop") // nil tolerated
	s.TrackMessageUsage(&stream.Usage{Input: 1, TotalTokens: 1, Cost: stream.Cost{Total: 0.01}}, "p", "m", "stop")

	sum := s.snapshot("/r", exitErrored)
	if !sum.Partial {
		t.Fatal("expected Partial=true with no agent_end")
	}
	if got, want := sum.Tokens.Input, 8; got != want {
		t.Errorf("partial input = %d, want %d", got, want)
	}
	if got, want := sum.Cost, 0.03; got != want {
		t.Errorf("partial cost = %v, want %v", got, want)
	}
}

// TestStats_CostIsCoherentJSONNumber confirms the results.jsonl cost
// field is a plain JSON number that round-trips.
func TestStats_CostIsCoherentJSONNumber(t *testing.T) {
	s := newStats("m", pinnedClock(), "")
	s.tallyAgentEnd(stream.AgentEnd{Messages: []stream.PiMessage{
		assistantMsg("p", "m", &stream.Usage{Input: 1, TotalTokens: 1, Cost: stream.Cost{Total: 1.25}}),
	}})
	sum := s.snapshot("/r", exitDone)

	enc, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(enc), `"cost":1.25`) {
		t.Errorf("expected cost as a JSON number 1.25, got: %s", enc)
	}
	var got summary
	if err := json.Unmarshal(enc, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Cost != sum.Cost {
		t.Errorf("round-trip Cost = %v, want %v", got.Cost, sum.Cost)
	}
}

// TestStats_PanelLayout asserts the operator-facing panel carries the
// expected sections under the pi schema.
func TestStats_PanelLayout(t *testing.T) {
	s := newStats("gpt-5.3-codex", pinnedClock(), "")
	s.iterations = 2
	s.TallyEvent(stream.TypeMessageEnd)
	s.TallyEvent(stream.TypeMessageEnd)
	s.TallyEvent(stream.TypeAgentEnd)
	s.TallyEvent("compaction_start") // known-but-unused -> alphabetical tail
	s.TallyBlock(stream.BlockText)
	s.TallyBlock(stream.BlockToolCall)
	s.TrackToolOutcome("bash", false)
	s.TrackToolOutcome("edit", true)
	s.tallyAgentEnd(stream.AgentEnd{Messages: []stream.PiMessage{
		assistantMsg("openai-codex", "gpt-5.3-codex", &stream.Usage{
			Input: 12_345, Output: 10, TotalTokens: 12_355, Cost: stream.Cost{Total: 0.5},
		}),
	}})
	s.AddLLMTime(2 * time.Second)
	s.AddToolTime(time.Second)

	var buf bytes.Buffer
	s.snapshot("/some/reqs", exitDone).writeText(&buf, 0)
	out := buf.String()

	wantSubstrings := []string{
		ui.RuleChar + ui.RuleChar + ui.RuleChar, // unicode rule appears
		"reqs:        /some/reqs",
		"model:       gpt-5.3-codex",
		"exit:        done",
		"iterations:  2",
		"message_end        2",
		"agent_end          1",
		"compaction_start   1",
		"text               1",
		"toolCall           1",
		"calls              2",
		"errors             1",
		"input:        12,345",
		"cost:        $",
		"by model:",
		"openai-codex/gpt-5.3-codex",
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
	if strings.Contains(out, "====") {
		t.Errorf("panel still uses the old ASCII rule:\n%s", out)
	}
	if strings.Contains(out, "engine:") || strings.Contains(out, "effort:") {
		t.Errorf("pi panel must not carry engine/effort lines:\n%s", out)
	}
}

func TestStats_PanelOmitsExitWhenEmpty(t *testing.T) {
	s := newStats("m", pinnedClock(), "")
	var buf bytes.Buffer
	s.snapshot("/some/reqs", exitNone).writeText(&buf, 0)
	if strings.Contains(buf.String(), "exit:") {
		t.Errorf("expected no exit line, got panel:\n%s", buf.String())
	}
}

func TestAppendResultsJSONL_CreatesDirAndAppends(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".ralph-loops")

	first := summary{Reqs: "/r", Exit: "done", Iterations: 1}
	second := summary{Reqs: "/r", Exit: "timeout", Iterations: 7}
	appendResultsJSONL(dir, first)
	appendResultsJSONL(dir, second)

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
	// Should not panic or otherwise misbehave.
	appendResultsJSONL("", summary{Reqs: "/r"})
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
	appendResultsJSONL(filepath.Join(parent, "child"), summary{Reqs: "/r"})

	if _, err := os.Stat(filepath.Join(parent, "child")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected child dir to be absent, stat err = %v", err)
	}
}

func TestStats_SnapshotShape(t *testing.T) {
	s := newStats("gpt-5.3-codex", pinnedClock(), "")
	s.iterations = 3
	s.TallyEvent(stream.TypeMessageEnd)
	s.TallyBlock(stream.BlockText)
	s.tallyAgentEnd(stream.AgentEnd{Messages: []stream.PiMessage{
		assistantMsg("p", "gpt-5.3-codex", &stream.Usage{Input: 100, Output: 50, TotalTokens: 150, Cost: stream.Cost{Total: 0.4}}),
	}})
	s.AddLLMTime(5 * time.Second)
	s.AddToolTime(2 * time.Second)

	sum := s.snapshot("/path/to/reqs", exitDone)
	if sum.Reqs != "/path/to/reqs" {
		t.Errorf("Reqs = %q", sum.Reqs)
	}
	if sum.Model != "gpt-5.3-codex" {
		t.Errorf("Model = %q", sum.Model)
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
	if sum.Cost != 0.4 {
		t.Errorf("Cost = %v, want 0.4", sum.Cost)
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
	s.TallyEvent(stream.TypeMessageEnd)
	if sum.Events[stream.TypeMessageEnd] != 1 {
		t.Errorf("snapshot Events should be cloned; got %v", sum.Events)
	}
}
