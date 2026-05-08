package loop

import (
	"bytes"
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
	s.writePanel(&buf, "done")
	out := buf.String()

	wantSubstrings := []string{
		"=" + strings.Repeat("=", statsDividerWidth-1),
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
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(out, sub) {
			t.Errorf("panel missing %q\n--- panel ---\n%s", sub, out)
		}
	}
}

func TestStats_PanelOmitsExitWhenEmpty(t *testing.T) {
	s := newStats("opus")
	var buf bytes.Buffer
	s.writePanel(&buf, "")
	if strings.Contains(buf.String(), "exit:") {
		t.Errorf("expected no exit line, got panel:\n%s", buf.String())
	}
}
