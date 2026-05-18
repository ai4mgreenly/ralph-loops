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

	// turns counts assistant turns across the run. pi's [stream.Usage]
	// is per-turn (one assistant message == one turn), so the count is
	// the number of usage-bearing assistant messages folded in — from
	// the authoritative agent_end transcripts, or from the live
	// [stats.TrackMessageUsage] calls in the partial fallback.
	turns        int
	partialTurns int

	// stopReasons tallies the pi turn stop reason
	// (stop|length|toolUse|error|aborted) by value, and lastStopReason
	// keeps the terminal/most-recent one for the headline. Both come
	// from the authoritative agent_end assistant messages; the partial
	// fallback fills them from [stats.TrackMessageUsage] instead so a
	// process that dies before agent_end still reports an honest reason.
	stopReasons       map[string]int
	lastStopReason    string
	partialStops      map[string]int
	partialLastReason string

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
		model:        model,
		now:          now,
		resultsHome:  resultsHome,
		startTime:    now(),
		events:       make(map[string]int),
		blocks:       make(map[string]int),
		byModel:      make(map[modelKey]*modelTally),
		stopReasons:  make(map[string]int),
		partialStops: make(map[string]int),
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
// provider/model are accepted to satisfy the [render.Recorder] contract
// but the partial fallback does not group by model. stopReason IS
// retained here: it is part of the Q6 surface and the only place a
// stop reason can be observed when the process dies before agent_end.
func (s *stats) TrackMessageUsage(u *stream.Usage, _, _, stopReason string) {
	if u == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partialTokens.add(tokensFromUsage(u))
	s.partialCost += u.Cost.Total
	// One usage-bearing assistant message == one pi turn.
	s.partialTurns++
	if stopReason != "" {
		s.partialStops[stopReason]++
		s.partialLastReason = stopReason
	}
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
		// pi's usage is per-turn, so each usage-bearing assistant
		// message is one turn.
		s.turns++

		// stopReason is part of the Q6 surface. Tally it by value so a
		// run that mixes reasons across turns/iterations reports an
		// honest distribution; keep the last one for the headline (it
		// reflects how the terminal turn actually ended).
		if m.StopReason != "" {
			s.stopReasons[m.StopReason]++
			s.lastStopReason = m.StopReason
		}

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
// text panel and as one JSON line in the results.jsonl log. The JSON
// shape is the exact machine-readable mirror of the panel's sections so
// a downstream tool can reconstruct the report verbatim. There is no
// backward-compat guarantee — the schema is whatever the panel needs.
//
// Schema (results.jsonl, one object per line):
//
//   - reqs        — absolute path to the reqs directory the run drove.
//   - model       — the operator-requested model label (banner only;
//     cost is pi's, not derived from this name). Omitted when unset.
//   - exit        — how the run terminated (done|timeout|errored|"").
//     Omitted when empty (no terminal classification).
//   - iterations  — number of loop iterations attempted.
//   - turns       — pi assistant turns across the run (one usage-bearing
//     assistant message == one turn).
//   - events      — wire-event type → count (mirrors the events panel).
//   - blocks      — assistant content-block type → count.
//   - tool_calls  — completed tool executions.
//   - tool_errors — the subset that reported isError.
//   - stop_reason — the terminal/most-recent pi stop reason
//     (stop|length|toolUse|error|aborted), "" if none was observed.
//   - stop_reasons — stop reason → count; present only when more than
//     one distinct reason occurred (the panel shows a tally only then).
//   - tokens      — {input,output,cache_read,cache_write,total}; pi's
//     own per-turn token accounting summed over the run.
//   - cost        — run cost in fractional USD, a plain JSON number,
//     copied verbatim from pi's per-turn accounting (no pricing table).
//   - by_model    — per-(provider,effective-model) breakdown; each row
//     carries its own tokens object and cost number plus the secondary
//     api attribute. Always present (>=1 row whenever any usage was
//     folded in) so a multi-provider run is never collapsed.
//   - partial     — true when no terminal agent_end was ever folded in,
//     so every figure above is the best-effort partial sum of the
//     assistant message_end usages. Omitted (false) on a clean run.
//   - time        — ralph's own engine-agnostic wall clock:
//     {start,end,llm_seconds,tools_seconds,other_seconds,total_seconds}.
type summary struct {
	Reqs       string         `json:"reqs"`
	Model      string         `json:"model,omitempty"`
	Exit       string         `json:"exit,omitempty"`
	Iterations int            `json:"iterations"`
	Turns      int            `json:"turns"`
	Events     map[string]int `json:"events"`
	Blocks     map[string]int `json:"blocks"`
	ToolCalls  int            `json:"tool_calls"`
	ToolErrors int            `json:"tool_errors"`
	// StopReason is the terminal/most-recent pi stop reason; "" when no
	// assistant message carried one.
	StopReason string `json:"stop_reason,omitempty"`
	// StopReasons is the by-value tally, emitted only when the run mixed
	// more than one distinct reason (it is redundant with StopReason for
	// a single-reason run, which the panel and schema both elide).
	StopReasons map[string]int `json:"stop_reasons,omitempty"`
	Tokens      summaryTokens  `json:"tokens"`
	// Cost is the run cost in fractional USD, copied verbatim from pi's
	// own per-turn accounting (ralph no longer prices tokens itself).
	Cost float64 `json:"cost"`
	// ByModel groups tokens and cost by (provider, effective-model, api)
	// so a multi-provider run is never collapsed into one number.
	ByModel []summaryModel `json:"by_model"`
	// Partial is true when no terminal agent_end was ever folded in, so
	// Tokens/Cost/Turns are the best-effort partial sum of the assistant
	// message_end usages (the process-died-early fallback).
	Partial bool        `json:"partial,omitempty"`
	Time    summaryTime `json:"time"`
}

type summaryTokens struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Total      int `json:"total"`
}

// summaryModel is one (provider, effective-model, api) row of the
// per-model breakdown, carrying that pair's own tokens and cost.
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
	turns := s.turns
	stops := s.stopReasons
	lastStop := s.lastStopReason
	partial := !s.agentEndSeen
	if partial {
		tk = s.partialTokens
		cost = s.partialCost
		turns = s.partialTurns
		stops = s.partialStops
		lastStop = s.partialLastReason
	}

	// The by-value stop tally is only meaningful (and only rendered)
	// when the run actually mixed reasons; for the common single-reason
	// run the headline StopReason already says everything.
	var stopTally map[string]int
	if len(stops) > 1 {
		stopTally = maps.Clone(stops)
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
		Reqs:        reqs,
		Model:       s.model,
		Exit:        exit.String(),
		Iterations:  s.iterations,
		Turns:       turns,
		Events:      maps.Clone(s.events),
		Blocks:      maps.Clone(s.blocks),
		ToolCalls:   s.toolCalls,
		ToolErrors:  s.toolErrors,
		StopReason:  lastStop,
		StopReasons: stopTally,
		Tokens:      toSummaryTokens(tk),
		Cost:        cost,
		ByModel:     byModel,
		Partial:     partial,
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

// modelLabel renders the operator-facing identifier for one per-model
// breakdown row: "provider/effective-model", with the secondary pi api
// attribute in parentheses when present (the same model can be served
// through different pi APIs, so it is shown but is not part of the
// single-vs-breakdown distinctness — that is keyed on the Q6
// (provider, effectiveModel) pair only).
func (m summaryModel) label() string {
	label := m.Provider + "/" + m.Model
	if m.API != "" {
		label += " (" + m.API + ")"
	}
	return label
}

// distinctPairs counts the distinct Q6 (provider, effectiveModel) pairs
// in the breakdown, ignoring the secondary api attribute. Exactly one
// pair ⇒ the panel renders a single concise cost row; more than one ⇒
// the full per-pair breakdown, so a multi-provider run is never
// collapsed into one number.
func distinctPairs(rows []summaryModel) int {
	seen := make(map[[2]string]struct{}, len(rows))
	for _, r := range rows {
		seen[[2]string{r.Provider, r.Model}] = struct{}{}
	}
	return len(seen)
}

// writeTokenBreakdown prints the five-stream pi token accounting under
// the given two-space-plus indent, used both for the run total and for
// each per-model row.
func writeTokenBreakdown(w io.Writer, indent string, t summaryTokens) {
	fmt.Fprintf(w, "%sinput:        %s\n", indent, ui.FormatNumber(t.Input))
	fmt.Fprintf(w, "%soutput:       %s\n", indent, ui.FormatNumber(t.Output))
	fmt.Fprintf(w, "%scache read:   %s\n", indent, ui.FormatNumber(t.CacheRead))
	fmt.Fprintf(w, "%scache write:  %s\n", indent, ui.FormatNumber(t.CacheWrite))
	fmt.Fprintf(w, "%stotal:        %s\n", indent, ui.FormatNumber(t.Total))
}

// writeText renders sum to w in the operator-facing panel format. It is
// the human-readable twin of the results.jsonl record: every section
// here has a [summary] field behind it and vice versa. width controls
// the bracketing horizontal rule; pass the value of [ui.Theme.Width],
// or 0 to fall back to [ui.RuleFallbackWidth].
//
// The cost section implements the Q6 rule: pi's authoritative USD total
// is always shown; then, if exactly one (provider, effectiveModel) pair
// drove the whole run, a single concise row; if it varied, the full
// per-pair breakdown (each pair its own token breakdown + cost) so a
// multi-provider run is never collapsed into one number.
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
	fmt.Fprintf(w, "iterations:  %d\n", sum.Iterations)
	fmt.Fprintf(w, "turns:       %d\n", sum.Turns)
	if sum.StopReason != "" {
		fmt.Fprintf(w, "stop reason: %s\n", sum.StopReason)
	}
	// The by-value tally is only carried when the run mixed reasons;
	// for a single-reason run the headline above already says it all.
	if len(sum.StopReasons) > 1 {
		fmt.Fprintln(w, "stop reasons:")
		reasons := make([]string, 0, len(sum.StopReasons))
		for k := range sum.StopReasons {
			reasons = append(reasons, k)
		}
		sort.Strings(reasons)
		for _, k := range reasons {
			fmt.Fprintf(w, "  %-*s %d\n", statsLabelWidth, k, sum.StopReasons[k])
		}
	}
	fmt.Fprintln(w)

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
	writeTokenBreakdown(w, "  ", sum.Tokens)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "cost:        %s\n", formatUSD(sum.Cost))
	switch {
	case len(sum.ByModel) == 0:
		// No usage was folded in at all (e.g. a run that produced no
		// assistant turns); the total above is the whole story.
	case distinctPairs(sum.ByModel) == 1:
		// Exactly one (provider, effectiveModel) pair drove the run:
		// one concise row is enough, no redundant breakdown.
		m := sum.ByModel[0]
		fmt.Fprintf(w, "  %s  tokens=%s cost=%s\n",
			m.label(), ui.FormatNumber(m.Tokens.Total), formatUSD(m.Cost))
	default:
		// Multiple pairs: the full per-pair breakdown, each with its
		// own token breakdown and cost — never collapsed.
		fmt.Fprintln(w, "by model:")
		for _, m := range sum.ByModel {
			fmt.Fprintf(w, "  %s  cost=%s\n", m.label(), formatUSD(m.Cost))
			writeTokenBreakdown(w, "    ", m.Tokens)
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
