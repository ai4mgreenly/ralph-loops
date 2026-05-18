package loop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
	// Turn count and stop reason are part of the Q6 surface and must
	// still be honestly reported from the partial fallback: two
	// usage-bearing messages (the nil one does not count) and the
	// last-seen stop reason.
	if got, want := sum.Turns, 2; got != want {
		t.Errorf("partial turns = %d, want %d", got, want)
	}
	if got, want := sum.StopReason, "stop"; got != want {
		t.Errorf("partial stop_reason = %q, want %q", got, want)
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

// TestStats_PanelLayout_SinglePair asserts the enriched Q6 panel for
// the common case: exactly one (provider, effectiveModel) pair drove
// the run, so the cost section is a single concise row (no redundant
// "by model:" breakdown), and the new turns / stop-reason headline and
// the full labelled token breakdown all render. Volatile values are
// pinned via pinnedClock so only structure is asserted (Q14c).
func TestStats_PanelLayout_SinglePair(t *testing.T) {
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
	m1 := assistantMsg("openai-codex", "gpt-5.3-codex", &stream.Usage{
		Input: 12_345, Output: 10, CacheRead: 7, CacheWrite: 3, TotalTokens: 12_365, Cost: stream.Cost{Total: 0.5},
	})
	m1.StopReason = "stop"
	s.tallyAgentEnd(stream.AgentEnd{Messages: []stream.PiMessage{m1}})
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
		"turns:       1",
		"stop reason: stop",
		"message_end        2",
		"agent_end          1",
		"compaction_start   1",
		"text               1",
		"toolCall           1",
		"calls              2",
		"errors             1",
		"tokens:",
		"  input:        12,345",
		"  output:       10",
		"  cache read:   7",
		"  cache write:  3",
		"  total:        12,365",
		"cost:        $0.5000",
		// Single pair -> concise row, NOT a "by model:" block.
		"  openai-codex/gpt-5.3-codex  tokens=12,365 cost=$0.5000",
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
	if strings.Contains(out, "by model:") {
		t.Errorf("single (provider,model) pair must render a concise row, not a by-model block:\n%s", out)
	}
	if strings.Contains(out, "====") {
		t.Errorf("panel still uses the old ASCII rule:\n%s", out)
	}
	if strings.Contains(out, "engine:") || strings.Contains(out, "effort:") {
		t.Errorf("pi panel must not carry engine/effort lines:\n%s", out)
	}
	if strings.Contains(out, "context") {
		t.Errorf("Q6 drops context-window %%; panel must not mention context:\n%s", out)
	}
}

// TestStats_PanelLayout_MultiPair asserts the Q6 rule that a run
// spanning more than one (provider, effectiveModel) pair is never
// collapsed: the cost section renders the full per-pair breakdown, each
// pair carrying its own labelled token breakdown and cost, and a mixed
// set of stop reasons surfaces as a small by-value tally.
func TestStats_PanelLayout_MultiPair(t *testing.T) {
	s := newStats("multi", pinnedClock(), "")
	s.iterations = 1

	a := assistantMsg("openai-codex", "gpt-5.3-codex", &stream.Usage{
		Input: 1000, Output: 200, CacheRead: 100, CacheWrite: 50, TotalTokens: 1350, Cost: stream.Cost{Total: 0.25},
	})
	a.API = "responses"
	a.StopReason = "toolUse"

	b := assistantMsg("anthropic", "claude-x", &stream.Usage{
		Input: 500, Output: 400, CacheRead: 25, CacheWrite: 75, TotalTokens: 1000, Cost: stream.Cost{Total: 0.125},
	})
	b.ResponseModel = "claude-x-2025"
	b.API = "messages"
	b.StopReason = "stop"

	s.tallyAgentEnd(stream.AgentEnd{Messages: []stream.PiMessage{a, b}})

	var buf bytes.Buffer
	s.snapshot("/some/reqs", exitDone).writeText(&buf, 0)
	out := buf.String()

	wantSubstrings := []string{
		"turns:       2",
		// Mixed reasons -> headline + by-value tally.
		"stop reasons:",
		"stop               1",
		"toolUse            1",
		"cost:        $0.3750", // 0.25 + 0.125
		"by model:",
		"  anthropic/claude-x-2025 (messages)  cost=$0.1250",
		"  openai-codex/gpt-5.3-codex (responses)  cost=$0.2500",
		// Each pair carries its own token breakdown indented one level
		// deeper than the run total.
		"    input:        1,000",
		"    input:        500",
		"    total:        1,350",
		"    total:        1,000",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(out, sub) {
			t.Errorf("multi-pair panel missing %q\n--- panel ---\n%s", sub, out)
		}
	}
}

// drainFixtureIntoStats feeds a testdata JSONL fixture through the REAL
// [stream.Reader] (the production decode path) and the REAL stats
// aggregation path: every event is tallied like the loop does and a
// terminal agent_end is folded via [stats.tallyAgentEnd]. It is the
// exact pump the loop runs, minus the subprocess and the renderer.
func drainFixtureIntoStats(t *testing.T, s *stats, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".jsonl"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	r := stream.NewReader(bytes.NewReader(b))
	for {
		ev, rErr := r.Next()
		if errors.Is(rErr, io.EOF) {
			break
		}
		if rErr != nil {
			var de *stream.DecodeError
			if errors.As(rErr, &de) {
				continue // forward-compat: tolerate, keep reading
			}
			t.Fatalf("stream read: %v", rErr)
		}
		s.TallyEvent(ev.Kind())
		if ae, ok := ev.(stream.AgentEnd); ok {
			s.tallyAgentEnd(ae)
		}
	}
}

// TestStats_ExactSumFromFixture is the Q14c deterministic exact-sum
// test. It feeds the hand-authored testdata/exact-sum.jsonl fixture
// (known fixed numbers, two distinct (provider, effectiveModel) pairs)
// through the real stream.Reader + stats aggregation and asserts the
// EXACT aggregated token sums, the EXACT total cost, and the EXACT
// per-(provider,effectiveModel) grouped sums.
//
// Fixture numbers (4 assistant messages; a user and a toolResult
// message are present and MUST NOT contribute):
//
//	A  openai-codex/gpt-5.3-codex  (api responses)  in1000 out200 cr100 cw50 tot1350 cost0.25  stop=toolUse
//	B  openai-codex/gpt-5.3-codex  (api responses)  in2000 out300 cr0   cw0  tot2300 cost0.50  stop=stop
//	C  anthropic/claude-x →claude-x-2025 (messages) in500  out400 cr25  cw75 tot1000 cost0.125 stop=toolUse
//	D  anthropic/claude-x →claude-x-2025 (messages) in1500 out600 cr0   cw0  tot2100 cost0.375 stop=stop
//
// All cost values are dyadic rationals (quarters/eighths) so every
// float64 sum below is EXACT — the equality assertions are honest, not
// epsilon-fudged. Aggregates auditable by inspection:
//
//	input 5000  output 1500  cacheRead 125  cacheWrite 125  total 6750
//	cost  1.25  (0.25+0.50+0.125+0.375)
//	openai-codex/gpt-5.3-codex : in3000 out500 cr100 cw50 tot3650 cost0.75 (0.25+0.50)
//	anthropic/claude-x-2025    : in2000 out1000 cr25 cw75 tot3100 cost0.50 (0.125+0.375)
//	turns 4   stop reasons {toolUse:2, stop:2}   last stop = "stop"
func TestStats_ExactSumFromFixture(t *testing.T) {
	s := newStats("gpt-5.3-codex", pinnedClock(), "")
	drainFixtureIntoStats(t, s, "exact-sum")
	sum := s.snapshot("/r", exitDone)

	if sum.Partial {
		t.Fatal("fixture ends in agent_end; summary must not be partial")
	}

	// Exact aggregated token sums.
	wantTokens := summaryTokens{Input: 5000, Output: 1500, CacheRead: 125, CacheWrite: 125, Total: 6750}
	if sum.Tokens != wantTokens {
		t.Errorf("tokens = %+v, want %+v", sum.Tokens, wantTokens)
	}

	// Exact total cost (dyadic rationals: this float64 equality holds).
	if sum.Cost != 1.25 {
		t.Errorf("cost = %v, want exactly 1.25", sum.Cost)
	}

	// Exact turn count: only the 4 assistant messages count.
	if sum.Turns != 4 {
		t.Errorf("turns = %d, want 4", sum.Turns)
	}

	// stopReason headline is the terminal one; mixed reasons -> tally.
	if sum.StopReason != "stop" {
		t.Errorf("stop_reason = %q, want %q", sum.StopReason, "stop")
	}
	if sum.StopReasons["toolUse"] != 2 || sum.StopReasons["stop"] != 2 {
		t.Errorf("stop_reasons = %v, want toolUse=2 stop=2", sum.StopReasons)
	}

	// Exact per-(provider, effectiveModel) grouped sums. snapshot sorts
	// by provider then model, so anthropic comes first.
	if len(sum.ByModel) != 2 {
		t.Fatalf("by_model rows = %d, want 2: %+v", len(sum.ByModel), sum.ByModel)
	}
	anth, oai := sum.ByModel[0], sum.ByModel[1]

	if anth.Provider != "anthropic" || anth.Model != "claude-x-2025" || anth.API != "messages" {
		t.Errorf("anthropic key = %+v", anth)
	}
	wantAnth := summaryTokens{Input: 2000, Output: 1000, CacheRead: 25, CacheWrite: 75, Total: 3100}
	if anth.Tokens != wantAnth {
		t.Errorf("anthropic tokens = %+v, want %+v", anth.Tokens, wantAnth)
	}
	if anth.Cost != 0.5 {
		t.Errorf("anthropic cost = %v, want exactly 0.5", anth.Cost)
	}

	if oai.Provider != "openai-codex" || oai.Model != "gpt-5.3-codex" || oai.API != "responses" {
		t.Errorf("openai key = %+v", oai)
	}
	wantOAI := summaryTokens{Input: 3000, Output: 500, CacheRead: 100, CacheWrite: 50, Total: 3650}
	if oai.Tokens != wantOAI {
		t.Errorf("openai tokens = %+v, want %+v", oai.Tokens, wantOAI)
	}
	if oai.Cost != 0.75 {
		t.Errorf("openai cost = %v, want exactly 0.75", oai.Cost)
	}

	// The grouped costs must sum back to the authoritative total, also
	// exactly (no rounding drift between the two views).
	if anth.Cost+oai.Cost != sum.Cost {
		t.Errorf("grouped cost sum %v != total %v", anth.Cost+oai.Cost, sum.Cost)
	}

	// The rendered panel must show the full per-pair breakdown (two
	// distinct pairs) and the exact pinned cost figures.
	var buf bytes.Buffer
	sum.writeText(&buf, 0)
	out := buf.String()
	for _, want := range []string{
		"cost:        $1.2500",
		"by model:",
		"  anthropic/claude-x-2025 (messages)  cost=$0.5000",
		"  openai-codex/gpt-5.3-codex (responses)  cost=$0.7500",
		"    total:        3,100",
		"    total:        3,650",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exact-sum panel missing %q\n--- panel ---\n%s", want, out)
		}
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
