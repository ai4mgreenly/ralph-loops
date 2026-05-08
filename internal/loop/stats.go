package loop

import (
	"fmt"
	"io"
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

// statsDividerWidth is the width of the leading and trailing rule that
// brackets the panel.
const statsDividerWidth = 70

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

// writePanel renders the closing summary block to w. exitReason is a
// short noun describing why the loop ended (e.g. "done", "timeout",
// "interrupted"); it may be empty if the panel is being printed mid-
// run for some reason.
func (s *stats) writePanel(w io.Writer, exitReason string) {
	elapsed := time.Since(s.startTime)
	other := elapsed - s.llmTime - s.toolTime
	if other < 0 {
		other = 0
	}
	divider := strings.Repeat("=", statsDividerWidth)

	fmt.Fprintln(w)
	fmt.Fprintln(w, divider)
	if exitReason != "" {
		fmt.Fprintf(w, "exit:        %s\n", exitReason)
	}
	fmt.Fprintf(w, "iterations:  %d\n\n", s.iterations)

	fmt.Fprintln(w, "events:")
	writeCountSection(w, s.events, orderedEventTypes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "blocks:")
	writeCountSection(w, s.blocks, orderedBlockTypes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "tokens:")
	fmt.Fprintf(w, "  input:        %s\n", ui.FormatNumber(s.tokens.input))
	fmt.Fprintf(w, "  cache read:   %s\n", ui.FormatNumber(s.tokens.cacheRead))
	fmt.Fprintf(w, "  cache create: %s\n", ui.FormatNumber(s.tokens.cacheCreate))
	fmt.Fprintf(w, "  output:       %s\n", ui.FormatNumber(s.tokens.output))
	fmt.Fprintf(w, "  total:        %s\n\n", ui.FormatNumber(s.tokens.total()))

	fmt.Fprintf(w, "cost:        $%.4f\n\n", s.cost)

	fmt.Fprintln(w, "time:")
	fmt.Fprintf(w, "  llm:    %s\n", ui.FormatElapsed(int(s.llmTime.Seconds())))
	fmt.Fprintf(w, "  tools:  %s\n", ui.FormatElapsed(int(s.toolTime.Seconds())))
	fmt.Fprintf(w, "  other:  %s\n", ui.FormatElapsed(int(other.Seconds())))
	fmt.Fprintf(w, "  total:  %s\n", ui.FormatElapsed(int(elapsed.Seconds())))

	fmt.Fprintln(w, divider)
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
