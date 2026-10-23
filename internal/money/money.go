// Package money reads the amounts people type into the portal: a member's
// monthly fee, a level's amount, a fee an admin adds by hand.
package money

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// MaxAmount is the largest amount in crowns the portal takes. It is the max
// of the fee input on the profile page; no level comes anywhere near it.
const MaxAmount = 255000

// ErrInvalid is returned for anything that is not a plain amount in range.
var ErrInvalid = errors.New("amount must be a number between 0 and 255000")

// plainAmount is digits with an optional decimal part: no sign, exponent,
// hex, underscores or words.
var plainAmount = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// ParseAmount reads a non-negative amount of crowns.
//
// strconv.ParseFloat alone is not enough, it takes "NaN" and "Inf". NaN fails
// every comparison, so a "fee < minimum" check lets it through, and SQLite
// casts the stored text "NaN" to 0 when it sums fees: the member pays nothing.
// fmt.Sscanf("%f") is worse still, it also stops at the first character it
// cannot use, so "1000abc" reads as 1000 and gets stored as typed.
func ParseAmount(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if !plainAmount.MatchString(s) {
		return 0, ErrInvalid
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v > MaxAmount {
		return 0, ErrInvalid
	}
	return v, nil
}

// FormatAmount is how an amount read by ParseAmount is stored: "1000",
// "1000.5", never "1e+03".
func FormatAmount(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
