package pricing

import "testing"

func TestModelsHasExpectedAliases(t *testing.T) {
	for _, alias := range []string{"haiku", "sonnet", "opus"} {
		if _, ok := Models[alias]; !ok {
			t.Errorf("Models missing alias %q", alias)
		}
	}
}

func TestModelsRatesAreSane(t *testing.T) {
	for alias, p := range Models {
		if p.Input <= 0 || p.Output <= 0 || p.CacheRead <= 0 || p.CacheCreate <= 0 {
			t.Errorf("%s: non-positive rate in %+v", alias, p)
		}
		if p.Output <= p.Input {
			t.Errorf("%s: output (%v) should exceed input (%v)", alias, p.Output, p.Input)
		}
		if p.CacheRead >= p.Input {
			t.Errorf("%s: cache-read (%v) should be cheaper than base input (%v)", alias, p.CacheRead, p.Input)
		}
	}
}

func TestModelsRelativeOrdering(t *testing.T) {
	haiku, sonnet, opus := Models["haiku"], Models["sonnet"], Models["opus"]
	if !(haiku.Input < sonnet.Input && sonnet.Input < opus.Input) {
		t.Errorf("input rates should ascend haiku<sonnet<opus, got %v %v %v",
			haiku.Input, sonnet.Input, opus.Input)
	}
	if !(haiku.Output < sonnet.Output && sonnet.Output < opus.Output) {
		t.Errorf("output rates should ascend haiku<sonnet<opus, got %v %v %v",
			haiku.Output, sonnet.Output, opus.Output)
	}
}
