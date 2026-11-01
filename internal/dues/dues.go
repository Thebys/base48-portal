// Package dues says where a member stands with the membership fee: what the
// monthly fee is, which month they have paid up to, which reminder that
// warrants, and what to suggest they pay. The monthly run, the reminders, the
// profile and the admin's previews all ask it, so they cannot disagree.
package dues

import (
	"fmt"
	"math"
	"time"

	"github.com/base48/member-portal/internal/money"
)

// Month is a calendar month.
type Month struct {
	Year  int
	Month time.Month
}

// MonthOf is the month of t in t's location.
func MonthOf(t time.Time) Month { return Month{t.Year(), t.Month()} }

// ParseMonth reads "2006-01", or the month of a "2006-01-02..." date.
func ParseMonth(s string) (Month, error) {
	if len(s) < 7 {
		return Month{}, fmt.Errorf("not a month: %q", s)
	}
	t, err := time.Parse("2006-01", s[:7])
	if err != nil {
		return Month{}, fmt.Errorf("not a month: %q", s)
	}
	return MonthOf(t), nil
}

// IsZero reports whether m is unset.
func (m Month) IsZero() bool { return m.Year == 0 }

// Add is the month n months after m (before, for negative n).
func (m Month) Add(n int) Month {
	return MonthOf(time.Date(m.Year, m.Month+time.Month(n), 1, 0, 0, 0, 0, time.UTC))
}

// Before reports whether m comes before o.
func (m Month) Before(o Month) bool {
	return m.Year < o.Year || (m.Year == o.Year && m.Month < o.Month)
}

// String is "2006-01".
func (m Month) String() string { return fmt.Sprintf("%04d-%02d", m.Year, int(m.Month)) }

// FeeAmount is what the monthly run charges a member, as stored: their own
// amount when it is a valid non-zero one, else their level's. fellBack says
// the member's own amount was set but not usable.
func FeeAmount(actual, level string) (amount string, fellBack bool, err error) {
	if v, err := money.ParseAmount(actual); err == nil && v != 0 {
		return actual, false, nil
	}
	if _, err := money.ParseAmount(level); err != nil {
		return "", false, fmt.Errorf("level amount %q: %w", level, err)
	}
	return level, actual != "" && actual != "0", nil
}

// MonthlyFee is FeeAmount as a number, 0 when nothing valid is set.
func MonthlyFee(actual, level string) float64 {
	amount, _, err := FeeAmount(actual, level)
	if err != nil {
		return 0
	}
	v, _ := money.ParseAmount(amount)
	return v
}

// Standing is a member's balance against their monthly fee.
type Standing struct {
	Fee     float64 // monthly fee
	Balance float64 // payments minus every fee charged so far
	// Anchor is the last month charged, or that would have been: the later
	// of the member's last fee and the last monthly run. The fee for it is
	// already in Balance, or was never due (the member joined after the run).
	Anchor Month
}

// Known reports whether there is a standing to speak of: a fee to measure
// against and a month to count from. Free memberships have none.
func (s Standing) Known() bool { return s.Fee > 0 && !s.Anchor.IsZero() }

// months is how many monthly fees the balance covers beyond the anchor,
// rounded down; negative when the member owes for the anchor month or before.
func (s Standing) months(extra float64) int {
	return int(math.Floor((s.Balance+extra)/s.Fee + 1e-9))
}

// PaidThrough is the last month the member has fully paid for.
func (s Standing) PaidThrough() Month { return s.Anchor.Add(s.months(0)) }

// Debt is what the member owes now, 0 when nothing.
func (s Standing) Debt() float64 { return math.Max(0, -s.Balance) }

// Short is what is missing to pay for the month after PaidThrough.
func (s Standing) Short() float64 {
	credit := s.Balance - float64(s.months(0))*s.Fee
	return math.Max(0, s.Fee-credit)
}

// Suggest is the payment that clears the debt and pays n months ahead.
// A credit is left as it is, so the amount stays a round one.
func (s Standing) Suggest(n int) float64 { return s.Debt() + float64(n)*s.Fee }

// Covers is the month the member will have paid up to after paying amount.
func (s Standing) Covers(amount float64) Month { return s.Anchor.Add(s.months(amount)) }

// AfterCharge is the standing once the fee for month m is charged too.
func (s Standing) AfterCharge(m Month) Standing {
	if !s.Anchor.Before(m) {
		return s
	}
	return Standing{Fee: s.Fee, Balance: s.Balance - s.Fee, Anchor: m}
}

// The membership reminders, as template keys.
const (
	None            = ""
	PrepaidEnding   = "prepaid_ending"   // paid up to the end of this month at most
	NegativeBalance = "negative_balance" // owes at least one monthly fee
	DebtWarning     = "debt_warning"     // owes at least two
)

// Policy is what the admin has set for the reminders.
type Policy struct {
	// Prepaid enables the reminder before any debt.
	Prepaid bool
	// MonthsAhead: the prepaid reminder goes to members who have not paid
	// for this many months after the current one. 1 means "paid up to the end
	// of this month at most".
	MonthsAhead int
}

// Classify says which reminder a standing warrants in month today. The debt
// reminders are as they always were: at least one fee owed, at least two.
func Classify(s Standing, today Month, p Policy) string {
	if !s.Known() {
		return None
	}
	switch {
	case s.Balance <= -2*s.Fee:
		return DebtWarning
	case s.Balance <= -s.Fee:
		return NegativeBalance
	case p.Prepaid && s.PaidThrough().Before(today.Add(max(1, p.MonthsAhead))):
		return PrepaidEnding
	}
	return None
}

// IsDebt reports whether tier is one of the debt reminders.
func IsDebt(tier string) bool { return tier == NegativeBalance || tier == DebtWarning }
