// Package pricing holds the per-token cost table used to estimate the
// price of a ralph run.
//
// Values are USD per million tokens for each Anthropic-published rate
// class. The aliases mirror what the claude CLI accepts: "haiku" resolves
// to the latest Haiku, "sonnet" to the latest Sonnet, and "opus" to the
// latest Opus. Refresh from
// https://platform.claude.com/docs/en/docs/about-claude/pricing when a
// new family ships.
package pricing

// Pricing captures the four billable rates for a model.
//
// All values are USD per one million tokens.
type Pricing struct {
	Input       float64 // base input tokens
	Output      float64 // generated output tokens
	CacheRead   float64 // tokens served from prompt cache
	CacheCreate float64 // tokens written into prompt cache
}

// Models maps each supported model alias to its [Pricing].
var Models = map[string]Pricing{
	"haiku":  {Input: 1.00, Output: 5.00, CacheRead: 0.10, CacheCreate: 1.25},
	"sonnet": {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheCreate: 3.75},
	"opus":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheCreate: 6.25},
}
