package fio

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// DayBalance is an account's balance at the end of a day.
type DayBalance struct {
	Day     string // YYYY-MM-DD
	Balance float64
}

// DailyBalances is what the statement says the account held: at the end of
// the day before it starts (its opening balance), at the end of each day money
// moved, and at the end of its last day. Days in between held the balance of
// the day before them.
//
// A statement whose transactions do not add up to its closing balance is
// incomplete, and DailyBalances refuses it rather than store wrong numbers.
func (r *FetchResult) DailyBalances() ([]DayBalance, error) {
	start, err := ParseDate(r.DateStart)
	if err != nil {
		return nil, fmt.Errorf("statement start %q: %w", r.DateStart, err)
	}
	end, err := ParseDate(r.DateEnd)
	if err != nil {
		return nil, fmt.Errorf("statement end %q: %w", r.DateEnd, err)
	}

	// Cents, so that thousands of additions do not drift.
	moved := make(map[string]int64)
	for _, tx := range r.Transactions {
		if len(tx.Date) < 10 {
			return nil, fmt.Errorf("transaction %d has no date", tx.ID)
		}
		moved[tx.Date[:10]] += toCents(tx.Amount)
	}
	days := make([]string, 0, len(moved))
	for day := range moved {
		days = append(days, day)
	}
	sort.Strings(days)

	balance := toCents(r.OpeningBalance)
	out := []DayBalance{{Day: start.AddDate(0, 0, -1).Format(time.DateOnly), Balance: fromCents(balance)}}
	for _, day := range days {
		balance += moved[day]
		out = append(out, DayBalance{Day: day, Balance: fromCents(balance)})
	}
	if balance != toCents(r.ClosingBalance) {
		return nil, fmt.Errorf("transactions add up to %.2f, the statement closes at %.2f: it is incomplete",
			fromCents(balance), r.ClosingBalance)
	}
	if last := end.Format(time.DateOnly); out[len(out)-1].Day != last {
		out = append(out, DayBalance{Day: last, Balance: fromCents(balance)})
	}
	return out, nil
}

func toCents(v float64) int64   { return int64(math.Round(v * 100)) }
func fromCents(c int64) float64 { return float64(c) / 100 }
