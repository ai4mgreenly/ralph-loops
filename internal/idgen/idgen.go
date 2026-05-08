// Package idgen mints requirement identifiers of the form R-XXXX-XXXX
// and inverts them back to the wall-clock instant at which they were
// minted.
//
// Each ID is derived from the wall-clock time at the moment of minting:
// the count of milliseconds since [Epoch] is run through an affine
// bijection over the integers modulo 36^8, then encoded in uppercase
// base-36, zero-padded to eight characters, and split into two
// dash-separated four-character groups.
//
// The bijection (a multiply-and-add modulo 36^8 with constants chosen
// to be coprime to the modulus) decorrelates adjacent timestamps so
// IDs look uncorrelated rather than running in obvious sequence. It is
// a permutation, not a hash with collisions: distinct inputs produce
// distinct outputs for the lifetime of one ~89-year cycle, and
// [TimeOf] inverts [NewAt] exactly within that cycle.
//
// Two calls in the same millisecond from independent processes still
// produce the same ID and therefore collide; at human keyboard pace
// that is effectively impossible.
//
// IDs are not chronologically sortable. If you need to order
// requirements by creation time, decode them with [TimeOf] or record
// the time alongside the ID.
package idgen

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// Epoch is the zero point for ID generation: 2025-01-01 00:00:00 UTC.
// A recent epoch keeps the ID space population small for ~89 years.
var Epoch = time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

// idWidth is the number of base-36 characters in the encoded body.
const idWidth = 8

// Affine-bijection constants.
//
// space is the modulus. It factors as 2^16 * 3^16, so any multiplier
// coprime to it must be odd and not divisible by 3.
//
// mixMultiplier is Knuth's "golden-ratio" constant 0x9E3779B1
// (2,654,435,761) — odd, not a multiple of 3, with a long-known
// reputation for distributing sequential inputs evenly through the
// output space.
//
// mixOffset is an arbitrary non-zero constant ("0xC0FFEE") so the
// epoch itself does not encode to all zeros.
const (
	mixMultiplier = 0x9E3779B1
	mixOffset     = 0xC0FFEE
)

// space, multiplier and offset are big.Int forms of the constants
// above. We use math/big because (ms * mixMultiplier) can exceed
// int64 by several bits before reduction modulo `space`; correctness
// matters more than the negligible cost of the allocation.
var (
	space      = new(big.Int).SetInt64(2_821_109_907_456) // 36^8
	multiplier = new(big.Int).SetInt64(mixMultiplier)
	offset     = new(big.Int).SetInt64(mixOffset)

	// inverseMultiplier is the modular multiplicative inverse of
	// multiplier modulo space, used by [TimeOf] to invert the
	// bijection. Computed once at package init; if multiplier and
	// space ever lose their coprime relationship the program will
	// fail to start, which is the correct outcome — every existing
	// ID would otherwise become irrecoverable.
	inverseMultiplier = mustModInverse(multiplier, space)
)

func mustModInverse(g, n *big.Int) *big.Int {
	inv := new(big.Int).ModInverse(g, n)
	if inv == nil {
		panic("idgen: multiplier and space are not coprime; bijection is broken")
	}
	return inv
}

// idPattern matches the canonical R-XXXX-XXXX form. Capture groups hold
// the two four-character base-36 halves.
var idPattern = regexp.MustCompile(`^R-([0-9A-Z]{4})-([0-9A-Z]{4})$`)

// ErrInvalidID is returned by [TimeOf] when its argument is not a
// well-formed identifier.
var ErrInvalidID = errors.New("invalid requirement ID")

// New returns a fresh ID derived from the current wall-clock time.
func New() string {
	return NewAt(time.Now())
}

// NewAt returns the ID corresponding to the given instant. Times
// before [Epoch] are clamped to the epoch itself.
func NewAt(t time.Time) string {
	ms := t.Sub(Epoch).Milliseconds()
	if ms < 0 {
		ms = 0
	}

	n := new(big.Int).SetInt64(ms)
	n.Mul(n, multiplier)
	n.Add(n, offset)
	n.Mod(n, space)

	body := strings.ToUpper(n.Text(36))
	if len(body) < idWidth {
		body = strings.Repeat("0", idWidth-len(body)) + body
	}
	return "R-" + body[:4] + "-" + body[4:]
}

// TimeOf inverts [NewAt]: given a canonical ID it returns the instant
// from which that ID was minted. Times outside the first ~89-year
// cycle following [Epoch] are not representable, so [TimeOf] always
// returns an instant within [Epoch, Epoch+cycle). For IDs minted
// during the first cycle that range is exactly the original time.
//
// An error wrapping [ErrInvalidID] is returned when id is not in the
// canonical R-XXXX-XXXX form.
func TimeOf(id string) (time.Time, error) {
	groups := idPattern.FindStringSubmatch(id)
	if groups == nil {
		return time.Time{}, fmt.Errorf("%w: expected R-XXXX-XXXX, got %q", ErrInvalidID, id)
	}

	body := groups[1] + groups[2]
	n, ok := new(big.Int).SetString(body, 36)
	if !ok {
		// The regex guarantees only base-36 digits reach this point,
		// so SetString cannot actually fail; the check is defence in
		// depth in case the pattern is loosened later.
		return time.Time{}, fmt.Errorf("%w: cannot decode body %q", ErrInvalidID, body)
	}

	n.Sub(n, offset)
	n.Mul(n, inverseMultiplier)
	n.Mod(n, space)

	// n is in [0, space), which is well within int64 range.
	ms := n.Int64()
	return Epoch.Add(time.Duration(ms) * time.Millisecond), nil
}
