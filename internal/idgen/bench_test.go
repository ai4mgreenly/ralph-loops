package idgen_test

import (
	"testing"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
)

// BenchmarkNewAt measures the throughput of the affine-bijection ID
// generator. The bijection runs through math/big to avoid overflow,
// so this benchmark serves as a guard against accidental regressions
// (e.g. a new prime allocation per call) more than as an absolute
// number to chase.
func BenchmarkNewAt(b *testing.B) {
	t := time.Date(2025, time.June, 1, 12, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		_ = idgen.NewAt(t)
	}
}
