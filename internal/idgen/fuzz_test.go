package idgen_test

import (
	"errors"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
)

// FuzzTimeOf asserts two contracts on the inverse encoder:
//
//  1. It never panics, regardless of input shape (well-formed or not).
//  2. For every input it accepts, formatting the recovered time
//     reproduces the original ID — i.e. the function is a true left
//     inverse of [idgen.NewAt] within one cycle.
//
// The seed corpus pairs known-good IDs with a sampling of malformed
// shapes the unit tests already cover; the fuzzer mutates from there.
func FuzzTimeOf(f *testing.F) {
	for _, s := range []string{
		"R-0007-J3LA",
		"R-0183-WVBZ",
		"R-AAAA-BBBB",
		"r-0000-0000",
		"",
		"R-0007-J3L",
		"R-XXXX-YYYY",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, id string) {
		got, err := idgen.TimeOf(id)
		if err != nil {
			if !errors.Is(err, idgen.ErrInvalidID) {
				t.Fatalf("non-ErrInvalidID error: %v", err)
			}
			return
		}
		// Successful parse: round-trip must reproduce the input
		// exactly. NewAt is deterministic and operates at ms
		// resolution; TimeOf returns ms-aligned instants, so the
		// round-trip is lossless within one cycle.
		round := idgen.NewAt(got)
		if round != id {
			t.Fatalf("round-trip mismatch: TimeOf(%q) -> %v -> NewAt -> %q", id, got, round)
		}
	})
}
