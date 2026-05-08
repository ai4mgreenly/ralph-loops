package pricing

import "testing"

func TestLookupHasExpectedAliases(t *testing.T) {
	for _, alias := range []string{"haiku", "sonnet", "opus"} {
		if _, ok := Lookup(alias); !ok {
			t.Errorf("Lookup missing alias %q", alias)
		}
	}
}

func TestLookupIsCaseInsensitiveAndTrimmed(t *testing.T) {
	for _, alias := range []string{"HAIKU", "  Sonnet ", "Opus"} {
		if _, ok := Lookup(alias); !ok {
			t.Errorf("Lookup(%q) should resolve via case-insensitive match", alias)
		}
	}
}

func TestLookupUnknownReturnsFalse(t *testing.T) {
	p, ok := Lookup("nonexistent")
	if ok {
		t.Errorf("expected ok=false for unknown alias, got %+v", p)
	}
	if (p != Pricing{}) {
		t.Errorf("expected zero Pricing for unknown alias, got %+v", p)
	}
}

func TestModelsRatesAreSane(t *testing.T) {
	forEachModel(func(alias string, p Pricing) {
		if p.Input <= 0 || p.Output <= 0 || p.CacheRead <= 0 || p.CacheCreate <= 0 {
			t.Errorf("%s: non-positive rate in %+v", alias, p)
		}
		if p.Output <= p.Input {
			t.Errorf("%s: output (%d) should exceed input (%d)", alias, p.Output, p.Input)
		}
		if p.CacheRead >= p.Input {
			t.Errorf("%s: cache-read (%d) should be cheaper than base input (%d)", alias, p.CacheRead, p.Input)
		}
	})
}

func TestModelsRelativeOrdering(t *testing.T) {
	haiku, _ := Lookup("haiku")
	sonnet, _ := Lookup("sonnet")
	opus, _ := Lookup("opus")
	if !(haiku.Input < sonnet.Input && sonnet.Input < opus.Input) {
		t.Errorf("input rates should ascend haiku<sonnet<opus, got %d %d %d",
			haiku.Input, sonnet.Input, opus.Input)
	}
	if !(haiku.Output < sonnet.Output && sonnet.Output < opus.Output) {
		t.Errorf("output rates should ascend haiku<sonnet<opus, got %d %d %d",
			haiku.Output, sonnet.Output, opus.Output)
	}
}
