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

	"github.com/ai4mgreenly/ralph-loops/internal/pricing"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// statsLabelWidth is the column width used for left-aligned labels in
// the events and blocks sections of the final panel. Mirrors the
// Ruby driver's ljust(18).
const statsLabelWidth = 18

// stats accumulates per-run telemetry across every iteration. The
// loop driver writes to it from one goroutine today, but the
// [render.Recorder] surface is exposed to a separate package and the
// embedded [sync.Mutex] guards every mutating method so a future
// renderer running concurrently is race-safe by construction. The
// in-process snapshot returns a value copy under the same lock so
// readers cannot observe a torn state.
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

	tokens tokens
	// cost is the running total in integer micro-USD (millionths of
	// a USD). Money is never represented as float64 here; conversion
	// to a float happens only at JSON-marshal and panel-render time.
	cost int64

	llmTime  time.Duration
	toolTime time.Duration
}

// tokens groups the four token streams claude reports per iteration.
type tokens struct {
	input       int
	output      int
	cacheRead   int
	cacheCreate int
}

func (t tokens) total() int {
	return t.input + t.output + t.cacheRead + t.cacheCreate
}

// orderedEventTypes is the print order for the events section. Events
// not in this list are still tallied and shown alphabetically below
// the known set.
var orderedEventTypes = []string{
	stream.TypeSystem,
	stream.TypeAssistant,
	stream.TypeUser,
	stream.TypeResult,
	stream.TypeRateLimit,
}

// orderedBlockTypes is the print order for the blocks section, with
// the same fall-through behavior as orderedEventTypes.
var orderedBlockTypes = []string{
	stream.BlockText,
	stream.BlockThinking,
	stream.BlockRedactedThinking,
	stream.BlockToolUse,
	stream.BlockToolResult,
}

// newStats returns a zero-valued accumulator anchored to now() and
// configured to compute cost against the named model. Unknown models
// produce zero-cost output; the operator still gets the token counts.
// resultsHome is the directory the closing panel's JSONL log gets
// written to; an empty string disables that log.
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
	}
}

// TallyBlock, AddLLMTime, AddToolTime, and TrackUsage are the
// [render.Recorder]-shaped methods the per-event renderer calls while
// pretty-printing the stream. They are exported solely so a sibling
// package (render) can satisfy that interface against the loop's
// unexported stats type. tallyEvent and incrementIteration stay
// unexported because they are driven by the loop itself, not render.
func (s *stats) tallyEvent(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[t]++
}

func (s *stats) TallyBlock(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks[t]++
}

func (s *stats) incrementIteration() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.iterations++
}

func (s *stats) AddLLMTime(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llmTime += d
}

func (s *stats) AddToolTime(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolTime += d
}

// TrackUsage rolls a single result event's usage into the running
// totals and updates the cost estimate using the pricing table.
func (s *stats) TrackUsage(u *stream.Usage) {
	if u == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens.input += u.InputTokens
	s.tokens.output += u.OutputTokens
	s.tokens.cacheRead += u.CacheReadInputTokens
	s.tokens.cacheCreate += u.CacheCreationInputTokens

	rate, ok := pricing.Lookup(s.model)
	if !ok {
		return
	}
	// Rates are micro-USD per million tokens. tokens * rate yields
	// micro-USD per million tokens-of-tokens, so we divide by one
	// million to land back in micro-USD. int64 throughout.
	s.cost += int64(u.InputTokens)*rate.Input/1_000_000 +
		int64(u.OutputTokens)*rate.Output/1_000_000 +
		int64(u.CacheReadInputTokens)*rate.CacheRead/1_000_000 +
		int64(u.CacheCreationInputTokens)*rate.CacheCreate/1_000_000
}

// formatUSD renders an integer micro-USD amount as "$X.YYYY" — four
// decimal places to match the existing on-screen panel format.
func formatUSD(microUSD int64) string {
	return fmt.Sprintf("$%.4f", float64(microUSD)/float64(pricing.MicroUSDPerUSD))
}

// costUSD is a run cost stored in integer micro-USD (millionths of a
// USD) but serialized to JSON as a USD float. The integer
// representation keeps the in-memory arithmetic exact; the float
// representation preserves the public results.jsonl schema, which has
// always exposed cost as a number-shaped "cost" field.
type costUSD int64

// MarshalJSON emits c as a USD float (micro-USD divided by
// [pricing.MicroUSDPerUSD]).
func (c costUSD) MarshalJSON() ([]byte, error) {
	return json.Marshal(float64(c) / float64(pricing.MicroUSDPerUSD))
}

// UnmarshalJSON accepts a USD float and stores it as integer micro-USD.
func (c *costUSD) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*c = costUSD(f * float64(pricing.MicroUSDPerUSD))
	return nil
}

// summary is the per-run record rendered both as the operator-facing
// text panel and as one line in the JSONL results log. The JSON shape
// mirrors the text panel's sections so a downstream tool can
// reconstruct the report.
type summary struct {
	Reqs       string         `json:"reqs"`
	Exit       string         `json:"exit,omitempty"`
	Iterations int            `json:"iterations"`
	Events     map[string]int `json:"events"`
	Blocks     map[string]int `json:"blocks"`
	Tokens     summaryTokens  `json:"tokens"`
	// Cost is the run cost in integer micro-USD; see [costUSD] for the
	// JSON shape (a float dollar amount under the "cost" key).
	Cost costUSD     `json:"cost"`
	Time summaryTime `json:"time"`
}

type summaryTokens struct {
	Input       int `json:"input"`
	CacheRead   int `json:"cache_read"`
	CacheCreate int `json:"cache_create"`
	Output      int `json:"output"`
	Total       int `json:"total"`
}

type summaryTime struct {
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	LLMSeconds   int       `json:"llm_seconds"`
	ToolsSeconds int       `json:"tools_seconds"`
	OtherSeconds int       `json:"other_seconds"`
	TotalSeconds int       `json:"total_seconds"`
}

// snapshot freezes the current accumulator state into a [summary]. It
// reads the wall clock to compute elapsed time, so callers should
// invoke it once at the end of the run. The maps are cloned under the
// lock so the returned value is fully independent of subsequent
// mutations.
func (s *stats) snapshot(reqs string, exit exitReason) summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.now()
	elapsed := end.Sub(s.startTime)
	other := elapsed - s.llmTime - s.toolTime
	if other < 0 {
		other = 0
	}
	return summary{
		Reqs:       reqs,
		Exit:       exit.String(),
		Iterations: s.iterations,
		Events:     maps.Clone(s.events),
		Blocks:     maps.Clone(s.blocks),
		Tokens: summaryTokens{
			Input:       s.tokens.input,
			CacheRead:   s.tokens.cacheRead,
			CacheCreate: s.tokens.cacheCreate,
			Output:      s.tokens.output,
			Total:       s.tokens.total(),
		},
		Cost: costUSD(s.cost),
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
// [ui.Theme.Width], or 0 to fall back to [ui.RuleFallbackWidth].
func (sum summary) writeText(w io.Writer, width int) {
	rule := ui.BuildRule(width)

	fmt.Fprintln(w)
	fmt.Fprintln(w, rule)
	fmt.Fprintf(w, "reqs:        %s\n", sum.Reqs)
	if sum.Exit != "" {
		fmt.Fprintf(w, "exit:        %s\n", sum.Exit)
	}
	fmt.Fprintf(w, "iterations:  %d\n\n", sum.Iterations)

	fmt.Fprintln(w, "events:")
	writeCountSection(w, sum.Events, orderedEventTypes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "blocks:")
	writeCountSection(w, sum.Blocks, orderedBlockTypes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "tokens:")
	fmt.Fprintf(w, "  input:        %s\n", ui.FormatNumber(sum.Tokens.Input))
	fmt.Fprintf(w, "  cache read:   %s\n", ui.FormatNumber(sum.Tokens.CacheRead))
	fmt.Fprintf(w, "  cache create: %s\n", ui.FormatNumber(sum.Tokens.CacheCreate))
	fmt.Fprintf(w, "  output:       %s\n", ui.FormatNumber(sum.Tokens.Output))
	fmt.Fprintf(w, "  total:        %s\n\n", ui.FormatNumber(sum.Tokens.Total))

	fmt.Fprintf(w, "cost:        %s\n\n", formatUSD(int64(sum.Cost)))

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
// [Config.ResultsHome] overrides this default.
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
