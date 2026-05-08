package idgen_test

import (
	"fmt"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
)

// ExampleNewAt mints an ID for a fixed instant. The output is stable
// across runs because NewAt is a pure function of its argument.
func ExampleNewAt() {
	t := time.Date(2025, time.June, 1, 12, 0, 0, 0, time.UTC)
	fmt.Println(idgen.NewAt(t))
	// Output: R-MXNM-7ZLA
}

// ExampleTimeOf demonstrates the round-trip property: NewAt and
// TimeOf are inverses for any instant in the first ~89-year cycle
// after [idgen.Epoch].
func ExampleTimeOf() {
	want := time.Date(2025, time.June, 1, 12, 0, 0, 0, time.UTC)
	id := idgen.NewAt(want)
	got, err := idgen.TimeOf(id)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(got.Equal(want))
	// Output: true
}
