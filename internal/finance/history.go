// Package finance is the association's money month by month: what members
// were charged and paid, what they owed at each month's end, and what the
// bank accounts held.
package finance

import (
	"time"

	"github.com/base48/member-portal/internal/db"
)

// Month is one month of the history.
type Month struct {
	Month    string   `json:"month"`   // 2026-10
	Charged  float64  `json:"charged"` // fees for the month
	Fees     int64    `json:"-"`       // how many
	Paid     float64  `json:"paid"`    // members' payments that month, with their own VS
	Payments int64    `json:"-"`       // how many
	Debt     float64  `json:"debt"`    // what members charged that month owed at its end
	Debtors  int      `json:"debtors"` // how many of them owed
	Balance  *Balance `json:"balance"` // on the accounts, nil before any statement
}

// Balance is what the accounts held together in a month, counting the end
// of each day. Rent leaves once a month, so the balance at the month's end
// swings by the rent depending on whether it left before the month ended or
// after; the average over the days does not.
type Balance struct {
	Avg  float64 `json:"avg"`  // over the days of the month, until today in the current one
	End  float64 `json:"end"`  // at the end of the month, or today
	Low  float64 `json:"low"`  // the lowest day
	High float64 `json:"high"` // the highest day
}

// History lists every month from the first fee or payment until the month of
// today, oldest first.
//
// Debt follows the portal's balance: a member's payments with their own VS
// minus their fees. A month counts those who were charged for it, so members
// long gone do not haunt every later month. For the current month it is what
// they owe now. Fees for later months are left out.
//
// The balance adds up what each account held at the end of each day, from
// balances ordered by day. An account the bank has not reported yet counts
// as empty.
func History(fees []db.ListMonthlyFeesByUserRow, payments []db.ListMonthlyMemberPaymentsByUserRow, balances []db.BankBalance, today time.Time) []Month {
	last := today.Format(monthLayout)
	type entry struct {
		user   int64
		count  int64
		amount float64
	}
	feesIn := make(map[string][]entry)
	paidIn := make(map[string][]entry)
	first := last
	for _, f := range fees {
		if f.Month > last || !validMonth(f.Month) {
			continue
		}
		feesIn[f.Month] = append(feesIn[f.Month], entry{f.UserID, f.FeeCount, f.Amount})
		first = min(first, f.Month)
	}
	for _, p := range payments {
		if p.Month > last || !validMonth(p.Month) {
			continue
		}
		paidIn[p.Month] = append(paidIn[p.Month], entry{p.UserID.Int64, p.PaymentCount, p.Amount})
		first = min(first, p.Month)
	}

	balance := make(map[int64]float64) // member: payments minus fees so far
	held := make(map[string]float64)   // account: balance at the end of the last day seen
	var total float64                  // the sum of held
	next := 0                          // first row of balances not applied yet
	apply := func(row db.BankBalance) {
		total += row.Balance - held[row.Account]
		held[row.Account] = row.Balance
	}
	var out []Month
	for month := first; month <= last; month = nextMonth(month) {
		m := Month{Month: month}
		for _, p := range paidIn[month] {
			balance[p.user] += p.amount
			m.Paid += p.amount
			m.Payments += p.count
		}
		for _, f := range feesIn[month] {
			balance[f.user] -= f.amount
			m.Charged += f.amount
			m.Fees += f.count
		}
		for _, f := range feesIn[month] {
			// Half a crown is rounding, not debt.
			if b := balance[f.user]; b < -0.5 {
				m.Debt -= b
				m.Debtors++
			}
		}

		// Statements from before the first month only set where it starts.
		for ; next < len(balances) && monthOf(balances[next].Day) < month; next++ {
			apply(balances[next])
		}
		// The month starts where the last one ended, if anything is known.
		// A day's balance holds from that day until the next change; the
		// average weighs each by its days.
		lastDay := daysIn(month)
		if month == last {
			lastDay = today.Day()
		}
		var b *Balance
		see := func(v float64) {
			if b == nil {
				b = &Balance{Low: v, High: v}
			}
			b.End, b.Low, b.High = v, min(b.Low, v), max(b.High, v)
		}
		known := len(held) > 0
		from, since := 1, 1 // first day known, day the current total holds since
		var sum float64     // total times days, from the first known day
		for next < len(balances) && monthOf(balances[next].Day) == month {
			day := balances[next].Day
			d := min(dayOf(day), lastDay)
			if known && d > since { // what last month left held some days
				sum += total * float64(d-since)
				see(total)
			}
			// All accounts' rows of one day at once: moving money between
			// them must not show as a dip.
			for ; next < len(balances) && balances[next].Day == day; next++ {
				apply(balances[next])
			}
			if !known {
				known, from = true, d
			}
			since = d
			see(total)
		}
		if known {
			sum += total * float64(lastDay-since+1)
			see(total)
			b.Avg = sum / float64(lastDay-from+1)
		}
		m.Balance = b
		out = append(out, m)
	}
	return out
}

// nextMonth returns the month after a YYYY-MM month.
func nextMonth(month string) string {
	t, _ := time.Parse(monthLayout, month)
	return t.AddDate(0, 1, 0).Format(monthLayout)
}

// daysIn returns how many days a YYYY-MM month has.
func daysIn(month string) int {
	t, _ := time.Parse(monthLayout, month)
	return t.AddDate(0, 1, -1).Day()
}

// dayOf returns the day of the month of a YYYY-MM-DD day, 1 if it has none.
func dayOf(day string) int {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return 1
	}
	return t.Day()
}

// monthOf returns the YYYY-MM month of a YYYY-MM-DD day.
func monthOf(day string) string {
	if len(day) < 7 {
		return ""
	}
	return day[:7]
}

// validMonth tells a YYYY-MM month from whatever else a date column holds.
func validMonth(month string) bool {
	_, err := time.Parse(monthLayout, month)
	return err == nil
}

const monthLayout = "2006-01"
