package loop

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/pricing"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// statsLabelWidth is the column width used for left-aligned labels in
// the events and blocks sections of the final panel. Mirrors the
// Ruby driver's ljust(18).
const statsLabelWidth = 18

// statsRuleFallbackWidth is the rule width used when the terminal
// width is unknown (output piped, NO_TERM, etc). Matches the historic
// fixed-width rule.
const statsRuleFallbackWidth = 70

// statsRuleChar is the unicode horizontal rule character used to
// bracket the panel.
const statsRuleChar = "─"

// stats accumulates per-run telemetry across every iteration. It is
// not safe for concurrent use; the iteration driver pushes updates
// from a single goroutine, then the outer loop reads it once at the
// end to render the panel.
type stats struct {
	model     string
	startTime time.Time

	iterations int

	events map[string]int
	blocks map[string]int

	tokens tokens
	cost   float64

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

// newStats returns a zero-valued accumulator anchored to time.Now and
// configured to compute cost against the named model. Unknown models
// produce zero-cost output; the operator still gets the token counts.
func newStats(model string) *stats {
	return &stats{
		model:     model,
		startTime: time.Now(),
		events:    make(map[string]int),
		blocks:    make(map[string]int),
	}
}

func (s *stats) tallyEvent(t string)         { s.events[t]++ }
func (s *stats) tallyBlock(t string)         { s.blocks[t]++ }
func (s *stats) incrementIteration()         { s.iterations++ }
func (s *stats) addLLMTime(d time.Duration)  { s.llmTime += d }
func (s *stats) addToolTime(d time.Duration) { s.toolTime += d }

// trackUsage rolls a single result event's usage into the running
// totals and updates the cost estimate using the pricing table.
func (s *stats) trackUsage(u *stream.Usage) {
	if u == nil {
		return
	}
	s.tokens.input += u.InputTokens
	s.tokens.output += u.OutputTokens
	s.tokens.cacheRead += u.CacheReadInputTokens
	s.tokens.cacheCreate += u.CacheCreationInputTokens

	rate, ok := pricing.Models[s.model]
	if !ok {
		return
	}
	s.cost += float64(u.InputTokens)*rate.Input/1_000_000 +
		float64(u.OutputTokens)*rate.Output/1_000_000 +
		float64(u.CacheReadInputTokens)*rate.CacheRead/1_000_000 +
		float64(u.CacheCreationInputTokens)*rate.CacheCreate/1_000_000
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
	Cost       float64        `json:"cost"`
	Time       summaryTime    `json:"time"`
}

type summaryTokens struct {
	Input       int `json:"input"`
	CacheRead   int `json:"cache_read"`
	CacheCreate int `json:"cache_create"`
	Output      int `json:"output"`
	Total       int `json:"total"`
}

type summaryTime struct {
	LLMSeconds   int `json:"llm_seconds"`
	ToolsSeconds int `json:"tools_seconds"`
	OtherSeconds int `json:"other_seconds"`
	TotalSeconds int `json:"total_seconds"`
}

// snapshot freezes the current accumulator state into a [summary]. It
// reads the wall clock to compute elapsed time, so callers should
// invoke it once at the end of the run.
func (s *stats) snapshot(reqs, exitReason string) summary {
	elapsed := time.Since(s.startTime)
	other := elapsed - s.llmTime - s.toolTime
	if other < 0 {
		other = 0
	}
	return summary{
		Reqs:       reqs,
		Exit:       exitReason,
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
		Cost: s.cost,
		Time: summaryTime{
			LLMSeconds:   int(s.llmTime.Seconds()),
			ToolsSeconds: int(s.toolTime.Seconds()),
			OtherSeconds: int(other.Seconds()),
			TotalSeconds: int(elapsed.Seconds()),
		},
	}
}

// writePanel renders the closing summary block to w. exitReason is a
// short noun describing why the loop ended (e.g. "done", "timeout",
// "interrupted"); it may be empty if the panel is being printed mid-
// run for some reason. reqs is the requirements path shown at the top
// of the panel.
func (s *stats) writePanel(w io.Writer, reqs, exitReason string) {
	s.snapshot(reqs, exitReason).writeText(w)
}

// writeText renders sum to w in the operator-facing panel format.
func (sum summary) writeText(w io.Writer) {
	rule := buildRule()

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

	fmt.Fprintf(w, "cost:        $%.4f\n\n", sum.Cost)

	fmt.Fprintln(w, "time:")
	fmt.Fprintf(w, "  llm:    %s\n", ui.FormatElapsed(sum.Time.LLMSeconds))
	fmt.Fprintf(w, "  tools:  %s\n", ui.FormatElapsed(sum.Time.ToolsSeconds))
	fmt.Fprintf(w, "  other:  %s\n", ui.FormatElapsed(sum.Time.OtherSeconds))
	fmt.Fprintf(w, "  total:  %s\n", ui.FormatElapsed(sum.Time.TotalSeconds))

	fmt.Fprintln(w, rule)
}

// buildRule returns a horizontal rule sized to the current terminal
// width, falling back to [statsRuleFallbackWidth] when the width is
// unknown (e.g. stdout is piped).
func buildRule() string {
	width := ui.TerminalWidth()
	if width <= 0 {
		width = statsRuleFallbackWidth
	}
	return strings.Repeat(statsRuleChar, width)
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

// resultsHomePath returns the directory that holds the JSONL log, or
// "" if the user's home directory cannot be determined. It is a var
// so tests can redirect writes to a temporary directory.
var resultsHomePath = defaultResultsHomePath

func defaultResultsHomePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ralph-loops")
}

// appendResultsJSONL appends one JSON line to ~/.ralph-loops/
// results.jsonl, creating the directory if necessary. Every failure
// mode — unknown home, mkdir denied, open denied, marshal error,
// short write — is swallowed: the JSONL log is best-effort
// observability and must never break a run.
func appendResultsJSONL(sum summary) {
	dir := resultsHomePath()
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
