package pricing

import "testing"

func TestLookup_KnownAliases(t *testing.T) {
	t.Parallel()
	for _, alias := range []string{"haiku", "sonnet", "opus"} {
		if _, ok := Lookup(alias); !ok {
			t.Errorf("Lookup missing alias %q", alias)
		}
	}
}

func TestLookup_CaseInsensitiveAndTrimmed(t *testing.T) {
	t.Parallel()
	for _, alias := range []string{"HAIKU", "  Sonnet ", "Opus"} {
		if _, ok := Lookup(alias); !ok {
			t.Errorf("Lookup(%q) should resolve via case-insensitive match", alias)
		}
	}
}

func TestLookup_UnknownAliasReturnsFalse(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("nonexistent")
	if ok {
		t.Errorf("expected ok=false for unknown alias, got %+v", p)
	}
	if (p != Pricing{}) {
		t.Errorf("expected zero Pricing for unknown alias, got %+v", p)
	}
}

func TestHasModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		alias string
		want  bool
	}{
		{"known/haiku", "haiku", true},
		{"known/sonnet", "Sonnet", true},
		{"known/opus-padded", "  opus  ", true},
		{"unknown", "gpt-4", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := HasModel(tc.alias); got != tc.want {
				t.Errorf("HasModel(%q) = %v, want %v", tc.alias, got, tc.want)
			}
		})
	}
}

func TestModels_RatesAreSane(t *testing.T) {
	t.Parallel()
	for alias, p := range models {
		if p.Input <= 0 || p.Output <= 0 || p.CacheRead <= 0 || p.CacheCreate <= 0 {
			t.Errorf("%s: non-positive rate in %+v", alias, p)
		}
		if p.Output <= p.Input {
			t.Errorf("%s: output (%d) should exceed input (%d)", alias, p.Output, p.Input)
		}
		if p.CacheRead >= p.Input {
			t.Errorf("%s: cache-read (%d) should be cheaper than base input (%d)", alias, p.CacheRead, p.Input)
		}
	}
}

func TestModels_RelativeOrdering(t *testing.T) {
	t.Parallel()
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
