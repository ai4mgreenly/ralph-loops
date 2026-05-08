package idgen

import (
	"errors"
	"regexp"
	"testing"
	"time"
)

// idShape matches a well-formed ID: the literal prefix R- followed by
// two dash-separated four-character uppercase base-36 groups.
var idShape = regexp.MustCompile(`^R-[0-9A-Z]{4}-[0-9A-Z]{4}$`)

// cycleMs is the size of one full ID cycle in milliseconds. It equals
// 36^8: the count of distinct points in the encoded space. After this
// many milliseconds since [Epoch], IDs start repeating.
const cycleMs int64 = 2_821_109_907_456

// TestGenerator_UsesInjectedClock verifies that the Generator mints
// IDs from its injected clock rather than the wall clock. This is the
// idiomatic Go pattern for testable time-dependent code: a function-
// valued field that defaults to time.Now when unset.
func TestGenerator_UsesInjectedClock(t *testing.T) {
	t.Parallel()

	fixed := Epoch.Add(42 * time.Millisecond)
	g := Generator{Now: func() time.Time { return fixed }}

	got := g.New()
	want := NewAt(fixed)
	if got != want {
		t.Errorf("g.New() = %q, want %q (NewAt(fixed))", got, want)
	}

	// Round-tripping through TimeOf must recover the injected instant.
	roundTripped, err := TimeOf(got)
	if err != nil {
		t.Fatalf("TimeOf: %v", err)
	}
	if !roundTripped.Equal(fixed) {
		t.Errorf("round-trip = %v, want %v", roundTripped, fixed)
	}
}

// TestGenerator_ZeroValueFallsBackToTimeNow asserts that a zero-value
// Generator still mints valid IDs without panicking — the convenience
// path that lets package-level New be implemented as a one-liner.
func TestGenerator_ZeroValueFallsBackToTimeNow(t *testing.T) {
	t.Parallel()
	var g Generator
	id := g.New()
	if !idShape.MatchString(id) {
		t.Errorf("zero-value Generator produced malformed ID: %q", id)
	}
}

// TestNewAt_PinnedValues anchors the ID generator against hand-computed
// expected outputs at the extreme points of the cycle. If any of these
// drift the bijection constants have been altered and any IDs minted
// against the previous constants will no longer round-trip.
func TestNewAt_PinnedValues(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{
			name: "at epoch",
			at:   Epoch,
			want: "R-0007-J3LA",
		},
		{
			name: "one millisecond after epoch",
			at:   Epoch.Add(time.Millisecond),
			want: "R-0183-WVBZ",
		},
		{
			name: "one millisecond before epoch clamps to epoch",
			at:   Epoch.Add(-time.Millisecond),
			want: "R-0007-J3LA",
		},
		{
			name: "exactly one cycle past epoch wraps to epoch ID",
			at:   Epoch.Add(time.Duration(cycleMs) * time.Millisecond),
			want: "R-0007-J3LA",
		},
		{
			name: "one millisecond past one full cycle equals 1ms-after-epoch",
			at:   Epoch.Add(time.Duration(cycleMs+1) * time.Millisecond),
			want: "R-0183-WVBZ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewAt(tc.at)
			if got != tc.want {
				t.Fatalf("NewAt(%s) = %q, want %q", tc.at, got, tc.want)
			}
		})
	}
}

// TestNewAt_ShapeAcrossExtremes verifies the output format at the
// boundary points of the cycle and well beyond it. Even far past the
// nominal lifetime of the scheme the encoder must still emit exactly
// eleven characters in canonical form.
func TestNewAt_ShapeAcrossExtremes(t *testing.T) {
	cycle := time.Duration(cycleMs) * time.Millisecond
	tests := []struct {
		name string
		at   time.Time
	}{
		{"epoch", Epoch},
		{"one ms after epoch", Epoch.Add(time.Millisecond)},
		{"recent date", time.Date(2026, time.May, 8, 12, 0, 0, 0, time.UTC)},
		{"last ms of first cycle", Epoch.Add(cycle - time.Millisecond)},
		{"first ms of second cycle", Epoch.Add(cycle)},
		{"middle of second cycle", Epoch.Add(cycle + cycle/2)},
		{"year 2200", time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NewAt(tc.at)
			if !idShape.MatchString(got) {
				t.Errorf("ID %q at %s does not match %s", got, tc.at, idShape)
			}
			if len(got) != 11 {
				t.Errorf("ID %q at %s has len %d, want 11", got, tc.at, len(got))
			}
		})
	}
}

// TestNewAt_DeterministicForSameInstant guards against any latent
// dependence on global mutable state inside the bijection.
func TestNewAt_DeterministicForSameInstant(t *testing.T) {
	at := time.Date(2026, time.May, 8, 12, 0, 0, 0, time.UTC)
	a := NewAt(at)
	b := NewAt(at)
	if a != b {
		t.Fatalf("NewAt is non-deterministic: %q vs %q", a, b)
	}
}

// TestNewAt_AdjacentInputsProduceDistinctOutputs is a smoke test for
// the bijective property: the affine permutation must never collapse
// two different inputs to the same output within a single cycle.
//
// We sample a stretch around the epoch rather than enumerate the full
// 2.8e12 element space; any collision in this many sequential mints
// would indicate a constant has lost its coprime relationship with the
// modulus.
func TestNewAt_AdjacentInputsProduceDistinctOutputs(t *testing.T) {
	const n = 10_000
	seen := make(map[string]int64, n)
	for i := int64(0); i < n; i++ {
		id := NewAt(Epoch.Add(time.Duration(i) * time.Millisecond))
		if prev, dup := seen[id]; dup {
			t.Fatalf("collision: ms=%d and ms=%d both produced %q", prev, i, id)
		}
		seen[id] = i
	}
}

// TestNewAt_DecorrelatesAdjacentInputs documents the visual-scrambling
// property: consecutive milliseconds should not yield consecutive IDs.
// We assert the first three encoded characters differ between adjacent
// mints, which is a low bar that pure timestamp encoding could not
// meet (it would always share its high-order prefix on adjacent ms).
func TestNewAt_DecorrelatesAdjacentInputs(t *testing.T) {
	a := NewAt(Epoch)
	b := NewAt(Epoch.Add(time.Millisecond))
	if a[2:5] == b[2:5] {
		t.Errorf("adjacent IDs share leading prefix: %q vs %q", a, b)
	}
}

// TestTimeOf_RoundTripsAcrossExtremes verifies that TimeOf inverts
// NewAt for every distinguishable case at the cycle boundaries.
func TestTimeOf_RoundTripsAcrossExtremes(t *testing.T) {
	cycle := time.Duration(cycleMs) * time.Millisecond
	tests := []struct {
		name string
		at   time.Time
	}{
		{"epoch", Epoch},
		{"one ms after epoch", Epoch.Add(time.Millisecond)},
		{"recent date", time.Date(2026, time.May, 8, 12, 34, 56, 789_000_000, time.UTC)},
		{"halfway through cycle", Epoch.Add(cycle / 2)},
		{"last ms of first cycle", Epoch.Add(cycle - time.Millisecond)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := NewAt(tc.at)
			got, err := TimeOf(id)
			if err != nil {
				t.Fatalf("TimeOf(%q) returned error: %v", id, err)
			}
			// NewAt operates at millisecond resolution, so the input is
			// rounded down to whole ms before encoding. Compare against
			// the same rounding to keep the assertion exact.
			want := tc.at.Truncate(time.Millisecond)
			if !got.Equal(want) {
				t.Fatalf("TimeOf(%q) = %s, want %s", id, got, want)
			}
		})
	}
}

// TestTimeOf_InvertsKnownEncodings pins the inverse against the same
// hand-computed fixtures as TestNewAt_PinnedValues.
func TestTimeOf_InvertsKnownEncodings(t *testing.T) {
	tests := []struct {
		id   string
		want time.Time
	}{
		{"R-0007-J3LA", Epoch},
		{"R-0183-WVBZ", Epoch.Add(time.Millisecond)},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			got, err := TimeOf(tc.id)
			if err != nil {
				t.Fatalf("TimeOf(%q) returned error: %v", tc.id, err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("TimeOf(%q) = %s, want %s", tc.id, got, tc.want)
			}
		})
	}
}

// TestTimeOf_FoldsCycleRollovers documents that IDs minted after a
// cycle has elapsed are inverted back into the *first* cycle. After
// 2114 the inverse becomes lossy in this sense.
func TestTimeOf_FoldsCycleRollovers(t *testing.T) {
	cycle := time.Duration(cycleMs) * time.Millisecond

	idAfterCycle := NewAt(Epoch.Add(cycle))
	got, err := TimeOf(idAfterCycle)
	if err != nil {
		t.Fatalf("TimeOf returned error: %v", err)
	}
	if !got.Equal(Epoch) {
		t.Fatalf("post-cycle ID inverted to %s, want %s (epoch)", got, Epoch)
	}
}

// TestTimeOf_RejectsInvalidInputs walks the boundaries of the input
// grammar.
func TestTimeOf_RejectsInvalidInputs(t *testing.T) {
	bad := []string{
		"",
		"R-0007-J3L",        // short half
		"R-0007-J3LAA",      // long half
		"r-0007-J3LA",       // lowercase prefix
		"R-0007J3LA",        // missing inner dash
		"X-0007-J3LA",       // wrong prefix letter
		"R-0007-J3LA ",      // trailing whitespace
		" R-0007-J3LA",      // leading whitespace
		"R-0007-J3La",       // lowercase digit
		"R-0007-J!LA",       // non-base36 character
		"R-0007-J3LA-extra", // trailing junk
	}
	for _, id := range bad {
		t.Run(id, func(t *testing.T) {
			_, err := TimeOf(id)
			if err == nil {
				t.Fatalf("TimeOf(%q) returned no error", id)
			}
			if !errors.Is(err, ErrInvalidID) {
				t.Fatalf("TimeOf(%q) error %v does not wrap ErrInvalidID", id, err)
			}
		})
	}
}
