// Package pricing holds the per-token cost table used to estimate the
// price of a ralph run.
//
// Values are micro-USD per million tokens. Aliases live in two flavors:
//
//   - Anthropic family aliases — "haiku", "sonnet", "opus" — match what
//     the claude CLI accepts (the latest model in each family). Refresh
//     from https://platform.claude.com/docs/en/docs/about-claude/pricing
//     when a new family ships.
//   - Vendor-specific model IDs — e.g. "gpt-5.5",
//     "gemini-3.1-pro-preview" — match what alternate engines forward
//     verbatim. OpenAI rates source from
//     https://developers.openai.com/api/docs/pricing; Google rates
//     from https://ai.google.dev/gemini-api/docs/pricing. Both vendors
//     bill cache reads but not per-token cache creation (Google bills
//     storage by hour instead), so CacheCreate is 0 for those rows.
//
// Unknown aliases are not an error: callers see ok=false from [Lookup]
// and skip cost accounting for that run.
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
	"haiku":                  {Input: 1_000_000, Output: 5_000_000, CacheRead: 100_000, CacheCreate: 1_250_000},
	"sonnet":                 {Input: 3_000_000, Output: 15_000_000, CacheRead: 300_000, CacheCreate: 3_750_000},
	"opus":                   {Input: 5_000_000, Output: 25_000_000, CacheRead: 500_000, CacheCreate: 6_250_000},
	"gpt-5.5":                {Input: 5_000_000, Output: 30_000_000, CacheRead: 500_000, CacheCreate: 0},
	"gemini-3.1-pro-preview": {Input: 2_000_000, Output: 12_000_000, CacheRead: 200_000, CacheCreate: 0},
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

// HasModel reports whether alias resolves to a known pricing entry.
// It is a thin convenience over [Lookup] for callers that only need
// the membership check, e.g. config validation at a package boundary.
func HasModel(alias string) bool {
	_, ok := Lookup(alias)
	return ok
}

// modelOrder is the canonical ordering for [Models], cheapest first.
// Hand-maintained alongside the [models] map so the help text and
// flag-allow-list iterate in the documented order rather than map-
// iteration order.
var modelOrder = []string{"haiku", "sonnet", "opus", "gpt-5.5", "gemini-3.1-pro-preview"}

// Models returns the set of known model aliases in cheapest-first
// order. Callers (notably the CLI flag layer) use it to keep their
// allow-list in lockstep with the pricing table; mutating the returned
// slice is safe because it is rebuilt on every call.
func Models() []string {
	out := make([]string, len(modelOrder))
	copy(out, modelOrder)
	return out
}
