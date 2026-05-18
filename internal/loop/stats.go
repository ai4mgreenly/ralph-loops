package loop

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// Compile-time proof that *stats satisfies the consumer-side
// [render.Recorder] seam: the emitter is handed *stats at construction
// and attributes timing/tallies through this interface without ever
// importing the loop's concrete type.
var _ render.Recorder = (*stats)(nil)

// statsLabelWidth is the column width used for left-aligned labels in
// the events and blocks sections of the final panel.
const statsLabelWidth = 18

// stats accumulates per-run telemetry across every iteration. The loop
// driver writes to it from one goroutine today, but the
// [render.Recorder] surface is exposed to a separate package and the
// embedded [sync.Mutex] guards every mutating method so a future
// renderer running concurrently is race-safe by construction. The
// in-process snapshot returns a value copy under the same lock so
// readers cannot observe a torn state.
//
// Under the pi migration cost is no longer derived from a local pricing
// table: pi reports a real fractional-USD figure per assistant message
// and that number is authoritative. The loop aggregates the per-turn
// usages of the terminal [stream.AgentEnd] into the run total (see
// [stats.tallyAgentEnd]); the [render.Recorder] usage hook only feeds
// the partial fallback used when the process dies before agent_end.
type stats struct {
	mu sync.Mutex

	model     string
	startTime time.Time

	// now is the wall-clock source. Held as a func so tests can pin
	// timestamps; production code passes [time.Now].
	now func() time.Time

	// resultsHome is the directory where the JSONL results log lives.
	// Empty disables logging.
	resultsHome string

	iterations int

	events map[string]int
	blocks map[string]int

	// toolCalls and toolErrors count completed tool executions and the
	// subset that reported failure, fed by [stats.TrackToolOutcome].
	toolCalls  int
	toolErrors int

	tokens tokens
	// cost is the running total in fractional USD. pi computes this
	// itself per provider/model; ralph no longer carries a pricing
	// table, so this float is simply the sum of pi's authoritative
	// per-turn cost numbers.
	cost float64

	// agentEndSeen is set the first time a terminal [stream.AgentEnd]
	// is folded in. While it is false the run total is the
	// best-effort partial sum of the assistant message_end usages seen
	// so far (the process-died-early fallback); once an agent_end is
	// folded in, that authoritative tally replaces the partial.
	agentEndSeen bool

	// partialTokens / partialCost accumulate the assistant message_end
	// usages observed live, so a process that dies before agent_end
	// still yields an honest (if partial) figure.
	partialTokens tokens
	partialCost   float64

	// byModel groups the authoritative agent_end tally by
	// (provider, effectiveModel) so the run summary retains the
	// grouped data even though the rich panel formatting is a later
	// slice.
	byModel map[modelKey]*modelTally

	llmTime  time.Duration
	toolTime time.Duration
}

// modelKey identifies one (provider, effectiveModel, api) grouping in
// the per-model breakdown. api is retained because the same model can
// be served through different pi APIs.
type modelKey struct {
	Provider string
	Model    string
	API      string
}

// modelTally is the token/cost subtotal for one [modelKey].
type modelTally struct {
	Tokens tokens
	Cost   float64
}

// tokens groups the four token streams pi reports per turn, plus the
// total pi itself reports (kept verbatim rather than recomputed so the
// figure always matches pi's own accounting).
type tokens struct {
	input      int
	output     int
	cacheRead  int
	cacheWrite int
	total      int
}

// add folds o into t in place.
func (t *tokens) add(o tokens) {
	t.input += o.input
	t.output += o.output
	t.cacheRead += o.cacheRead
	t.cacheWrite += o.cacheWrite
	t.total += o.total
}

// fromUsage projects a pi [stream.Usage] onto the loop's token shape.
func tokensFromUsage(u *stream.Usage) tokens {
	if u == nil {
		return tokens{}
	}
	return tokens{
		input:      u.Input,
		output:     u.Output,
		cacheRead:  u.CacheRead,
		cacheWrite: u.CacheWrite,
		total:      u.TotalTokens,
	}
}

// orderedEventTypes is the print order for the events section. Events
// not in this list are still tallied and shown alphabetically below
// the known set. The set tracks pi's event vocabulary; unknown/known-
// but-unused carriers fall through to the alphabetical tail.
var orderedEventTypes = []string{
	stream.TypeSession,
	stream.TypeMessageEnd,
	stream.TypeToolExecutionStart,
	stream.TypeToolExecutionEnd,
	stream.TypeTurnEnd,
	stream.TypeAgentEnd,
}

// orderedBlockTypes is the print order for the blocks section, with
// the same fall-through behavior as orderedEventTypes.
var orderedBlockTypes = []string{
	stream.BlockText,
	stream.BlockThinking,
	stream.BlockToolCall,
}

// newStats returns a zero-valued accumulator anchored to now() and
// labelled with the requested model (for the banner/panel only — cost
// comes from pi, not from the model name). resultsHome is the directory
// the closing panel's JSONL log gets written to; an empty string
// disables that log.
func newStats(model string, now func() time.Time, resultsHome string) *stats {
	if now == nil {
		now = time.Now
	}
	return &stats{
		model:       model,
		now:         now,
		resultsHome: resultsHome,
		startTime:   now(),
		events:      make(map[string]int),
		blocks:      make(map[string]int),
		byModel:     make(map[modelKey]*modelTally),
	}
}

// TallyEvent counts one decoded event of the given wire-format kind.
// It is part of the [render.Recorder] surface the emitter drives.
func (s *stats) TallyEvent(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[kind]++
}

// TallyBlock counts one assistant content block of the given type. It
// is part of the [render.Recorder] surface.
func (s *stats) TallyBlock(blockType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks[blockType]++
}

func (s *stats) incrementIteration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.iterations++
}

// AddLLMTime attributes d to model think/generate time. Part of the
// [render.Recorder] surface.
func (s *stats) AddLLMTime(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llmTime += d
}

// AddToolTime attributes d to tool-execution time. Part of the
// [render.Recorder] surface.
func (s *stats) AddToolTime(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolTime += d
}

// TrackMessageUsage captures one assistant message's per-turn usage as
// the process-died-early partial fallback. It is NOT the authoritative
// per-iteration tally — that comes from the terminal [stream.AgentEnd]
// via [stats.tallyAgentEnd]. A nil usage is tolerated and ignored.
// provider/model/stopReason are accepted to satisfy the
// [render.Recorder] contract; the partial fallback only needs the
// token/cost figures, so they are not retained here.
func (s *stats) TrackMessageUsage(u *stream.Usage, _, _, _ string) {
	if u == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partialTokens.add(tokensFromUsage(u))
	s.partialCost += u.Cost.Total
}

// TrackToolOutcome records one completed tool execution. Part of the
// [render.Recorder] surface.
func (s *stats) TrackToolOutcome(_ string, isError bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls++
	if isError {
		s.toolErrors++
	}
}

// tallyAgentEnd folds a terminal [stream.AgentEnd] into the run total.
// It is the single source of truth for tokens and cost (Q6): every
// assistant message in the transcript carries a per-turn (NOT
// cumulative) [stream.Usage], so the iteration figure is the sum over
// assistant messages, and the run total is the sum across iterations'
// agent_end events. The grouped (provider, effectiveModel, api) data is
// retained for the summary; pi's own fractional-USD cost is
// authoritative.
func (s *stats) tallyAgentEnd(ev stream.AgentEnd) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentEndSeen = true
	for i := range ev.Messages {
		m := &ev.Messages[i]
		if m.Role != stream.RoleAssistant || m.Usage == nil {
			continue
		}
		tk := tokensFromUsage(m.Usage)
		c := m.Usage.Cost.Total

		s.tokens.add(tk)
		s.cost += c

		key := modelKey{
			Provider: m.Provider,
			Model:    effectiveModel(m),
			API:      m.API,
		}
		mt := s.byModel[key]
		if mt == nil {
			mt = &modelTally{}
			s.byModel[key] = mt
		}
		mt.Tokens.add(tk)
		mt.Cost += c
	}
}

// effectiveModel is the Q6 "effective model": the model the provider
// actually served the request with (ResponseModel) when pi reports it,
// otherwise the requested Model.
func effectiveModel(m *stream.PiMessage) string {
	if m.ResponseModel != "" {
		return m.ResponseModel
	}
	return m.Model
}

// formatUSD renders a fractional-USD amount as "$X.YYYY" — four decimal
// places, matching the on-screen panel convention.
func formatUSD(usd float64) string {
	return fmt.Sprintf("$%.4f", usd)
}

// summary is the per-run record rendered both as the operator-facing
// text panel and as one line in the JSONL results log. The JSON shape
// mirrors the text panel's sections so a downstream tool can
// reconstruct the report. The schema changed under the pi migration
// (no engine/effort; cost is pi's fractional USD; a per-model
// breakdown was added) and carries no backward-compat guarantee.
type summary struct {
	Reqs       string         `json:"reqs"`
	Model      string         `json:"model"`
	Exit       string         `json:"exit,omitempty"`
	Iterations int            `json:"iterations"`
	Events     map[string]int `json:"events"`
	Blocks     map[string]int `json:"blocks"`
	ToolCalls  int            `json:"tool_calls"`
	ToolErrors int            `json:"tool_errors"`
	Tokens     summaryTokens  `json:"tokens"`
	// Cost is the run cost in fractional USD, copied verbatim from pi's
	// own per-turn accounting (ralph no longer prices tokens itself).
	Cost float64 `json:"cost"`
	// ByModel groups tokens and cost by (provider, model, api) so the
	// grouped data survives even though rich panel formatting is a
	// later slice.
	ByModel []summaryModel `json:"by_model"`
	// Partial is true when no terminal agent_end was ever folded in, so
	// Tokens/Cost are the best-effort partial sum of the assistant
	// message_end usages (the process-died-early fallback).
	Partial bool        `json:"partial,omitempty"`
	Time    summaryTime `json:"time"`
}

type summaryTokens struct {
	Input      int `json:"input"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Output     int `json:"output"`
	Total      int `json:"total"`
}

// summaryModel is one (provider, model, api) row of the per-model
// breakdown.
type summaryModel struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	API      string        `json:"api"`
	Tokens   summaryTokens `json:"tokens"`
	Cost     float64       `json:"cost"`
}

type summaryTime struct {
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	LLMSeconds   int       `json:"llm_seconds"`
	ToolsSeconds int       `json:"tools_seconds"`
	OtherSeconds int       `json:"other_seconds"`
	TotalSeconds int       `json:"total_seconds"`
}

// toSummaryTokens projects the internal token shape onto the JSON one.
func toSummaryTokens(t tokens) summaryTokens {
	return summaryTokens{
		Input:      t.input,
		CacheRead:  t.cacheRead,
		CacheWrite: t.cacheWrite,
		Output:     t.output,
		Total:      t.total,
	}
}

// snapshot freezes the current accumulator state into a [summary]. It
// reads the wall clock to compute elapsed time, so callers should
// invoke it once at the end of the run. The maps are cloned under the
// lock so the returned value is fully independent of subsequent
// mutations.
//
// When no terminal agent_end was ever folded in, the run total falls
// back to the partial sum of assistant message_end usages — partial but
// honest — and Partial is set so a downstream consumer can tell the
// difference.
func (s *stats) snapshot(reqs string, exit exitReason) summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.now()
	elapsed := end.Sub(s.startTime)
	other := elapsed - s.llmTime - s.toolTime
	if other < 0 {
		other = 0
	}

	tk := s.tokens
	cost := s.cost
	partial := !s.agentEndSeen
	if partial {
		tk = s.partialTokens
		cost = s.partialCost
	}

	byModel := make([]summaryModel, 0, len(s.byModel))
	for k, v := range s.byModel {
		byModel = append(byModel, summaryModel{
			Provider: k.Provider,
			Model:    k.Model,
			API:      k.API,
			Tokens:   toSummaryTokens(v.Tokens),
			Cost:     v.Cost,
		})
	}
	sort.Slice(byModel, func(i, j int) bool {
		if byModel[i].Provider != byModel[j].Provider {
			return byModel[i].Provider < byModel[j].Provider
		}
		if byModel[i].Model != byModel[j].Model {
			return byModel[i].Model < byModel[j].Model
		}
		return byModel[i].API < byModel[j].API
	})

	return summary{
		Reqs:       reqs,
		Model:      s.model,
		Exit:       exit.String(),
		Iterations: s.iterations,
		Events:     maps.Clone(s.events),
		Blocks:     maps.Clone(s.blocks),
		ToolCalls:  s.toolCalls,
		ToolErrors: s.toolErrors,
		Tokens:     toSummaryTokens(tk),
		Cost:       cost,
		ByModel:    byModel,
		Partial:    partial,
		Time: summaryTime{
			Start:        s.startTime,
			End:          end,
			LLMSeconds:   int(s.llmTime.Seconds()),
			ToolsSeconds: int(s.toolTime.Seconds()),
			OtherSeconds: int(other.Seconds()),
			TotalSeconds: int(elapsed.Seconds()),
		},
	}
}

// iterationCount returns the number of iterations attempted so far,
// taking the lock so a concurrent renderer or test cannot observe a
// torn write.
func (s *stats) iterationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.iterations
}

// writeText renders sum to w in the operator-facing panel format.
// width controls the bracketing horizontal rule; pass the value of
// [ui.Theme.Width], or 0 to fall back to [ui.RuleFallbackWidth]. The
// rich per-model panel is a later slice; here the breakdown is printed
// as a basic block so the grouped data is visible without being lost.
func (sum summary) writeText(w io.Writer, width int) {
	rule := ui.BuildRule(width)

	fmt.Fprintln(w)
	fmt.Fprintln(w, rule)
	fmt.Fprintf(w, "reqs:        %s\n", sum.Reqs)
	if sum.Model != "" {
		fmt.Fprintf(w, "model:       %s\n", sum.Model)
	}
	if sum.Exit != "" {
		fmt.Fprintf(w, "exit:        %s\n", sum.Exit)
	}
	if sum.Partial {
		fmt.Fprintln(w, "partial:     true (no agent_end observed; figures are a partial sum)")
	}
	fmt.Fprintf(w, "iterations:  %d\n\n", sum.Iterations)

	fmt.Fprintln(w, "events:")
	writeCountSection(w, sum.Events, orderedEventTypes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "blocks:")
	writeCountSection(w, sum.Blocks, orderedBlockTypes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "tools:")
	fmt.Fprintf(w, "  %-*s %d\n", statsLabelWidth, "calls", sum.ToolCalls)
	fmt.Fprintf(w, "  %-*s %d\n\n", statsLabelWidth, "errors", sum.ToolErrors)

	fmt.Fprintln(w, "tokens:")
	fmt.Fprintf(w, "  input:        %s\n", ui.FormatNumber(sum.Tokens.Input))
	fmt.Fprintf(w, "  cache read:   %s\n", ui.FormatNumber(sum.Tokens.CacheRead))
	fmt.Fprintf(w, "  cache write:  %s\n", ui.FormatNumber(sum.Tokens.CacheWrite))
	fmt.Fprintf(w, "  output:       %s\n", ui.FormatNumber(sum.Tokens.Output))
	fmt.Fprintf(w, "  total:        %s\n\n", ui.FormatNumber(sum.Tokens.Total))

	fmt.Fprintf(w, "cost:        %s\n", formatUSD(sum.Cost))
	if len(sum.ByModel) > 0 {
		fmt.Fprintln(w, "by model:")
		for _, m := range sum.ByModel {
			label := m.Provider + "/" + m.Model
			if m.API != "" {
				label += " (" + m.API + ")"
			}
			fmt.Fprintf(w, "  %s  tokens=%s cost=%s\n",
				label, ui.FormatNumber(m.Tokens.Total), formatUSD(m.Cost))
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "time:")
	fmt.Fprintf(w, "  start:  %s\n", sum.Time.Start.Format(time.RFC3339))
	fmt.Fprintf(w, "  end:    %s\n", sum.Time.End.Format(time.RFC3339))
	fmt.Fprintf(w, "  llm:    %s\n", ui.FormatElapsed(sum.Time.LLMSeconds))
	fmt.Fprintf(w, "  tools:  %s\n", ui.FormatElapsed(sum.Time.ToolsSeconds))
	fmt.Fprintf(w, "  other:  %s\n", ui.FormatElapsed(sum.Time.OtherSeconds))
	fmt.Fprintf(w, "  total:  %s\n", ui.FormatElapsed(sum.Time.TotalSeconds))

	fmt.Fprintln(w, rule)
}

// writeCountSection prints a labelled counts block, listing first the
// keys in `order` (always shown, even at zero) and then any leftover
// keys present in counts but not in order, sorted alphabetically.
func writeCountSection(w io.Writer, counts map[string]int, order []string) {
	known := make(map[string]bool, len(order))
	for _, k := range order {
		known[k] = true
		fmt.Fprintf(w, "  %-*s %d\n", statsLabelWidth, k, counts[k])
	}
	var extras []string
	for k := range counts {
		if !known[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	for _, k := range extras {
		fmt.Fprintf(w, "  %-*s %d\n", statsLabelWidth, k, counts[k])
	}
}

// defaultResultsHomePath returns the directory that holds the JSONL
// log, or "" if the user's home directory cannot be determined.
// [WithResultsHome] overrides this default.
func defaultResultsHomePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ralph-loops")
}

// appendResultsJSONL appends one JSON line to <dir>/results.jsonl,
// creating the directory if necessary. Every failure mode — empty
// dir, mkdir denied, open denied, marshal error, short write — is
// swallowed: the JSONL log is best-effort observability and must
// never break a run.
func appendResultsJSONL(dir string, sum summary) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(
		filepath.Join(dir, "results.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return
	}
	defer f.Close()

	enc, err := json.Marshal(sum)
	if err != nil {
		return
	}
	enc = append(enc, '\n')
	_, _ = f.Write(enc)
}
