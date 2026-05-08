// Package pricing holds the per-token cost table used to estimate the
// price of a ralph run.
//
// Values are micro-USD per million tokens for each Anthropic-published
// rate class. The aliases mirror what the claude CLI accepts: "haiku"
// resolves to the latest Haiku, "sonnet" to the latest Sonnet, and
// "opus" to the latest Opus. Refresh from
// https://platform.claude.com/docs/en/docs/about-claude/pricing when a
// new family ships.
package pricing

import "strings"

// MicroUSDPerUSD is the scale factor used to express dollar amounts as
// integer micro-USD (millionths of a dollar). Money math should never
// be done in float64; this constant exists so that conversions to and
// from human dollars are explicit and grep-able.
const MicroUSDPerUSD = 1_000_000

// Pricing captures the four billable rates for a model.
//
// All values are micro-USD per one million tokens. So a published rate
// of $3.00 / Mtok is stored as 3_000_000.
type Pricing struct {
	Input       int64 // base input tokens
	Output      int64 // generated output tokens
	CacheRead   int64 // tokens served from prompt cache
	CacheCreate int64 // tokens written into prompt cache
}

// models maps each supported model alias to its [Pricing]. Use
// [Lookup] to resolve aliases; the map itself is unexported so that
// callers cannot mutate the table at runtime.
var models = map[string]Pricing{
	"haiku":  {Input: 1_000_000, Output: 5_000_000, CacheRead: 100_000, CacheCreate: 1_250_000},
	"sonnet": {Input: 3_000_000, Output: 15_000_000, CacheRead: 300_000, CacheCreate: 3_750_000},
	"opus":   {Input: 5_000_000, Output: 25_000_000, CacheRead: 500_000, CacheCreate: 6_250_000},
}

// Lookup resolves a model alias to its [Pricing]. The match is
// case-insensitive and tolerates surrounding whitespace; an unknown
// alias yields the zero Pricing and ok=false so callers can decide
// whether to skip cost accounting.
func Lookup(alias string) (Pricing, bool) {
	key := strings.ToLower(strings.TrimSpace(alias))
	p, ok := models[key]
	return p, ok
}

// forEachModel invokes fn once for every alias in the table. It exists
// so that in-package tests can assert table-wide invariants without
// re-exporting the underlying map.
func forEachModel(fn func(alias string, p Pricing)) {
	for alias, p := range models {
		fn(alias, p)
	}
}
