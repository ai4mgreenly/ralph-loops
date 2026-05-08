package pricing_test

import (
	"fmt"

	"github.com/ai4mgreenly/ralph-loops/internal/pricing"
)

// ExampleLookup demonstrates the case-insensitive alias resolution
// and the per-token rate fields exposed on [pricing.Pricing].
func ExampleLookup() {
	p, ok := pricing.Lookup("Sonnet")
	if !ok {
		fmt.Println("unknown alias")
		return
	}
	fmt.Printf("input=%d output=%d\n", p.Input, p.Output)
	// Output: input=3000000 output=15000000
}
