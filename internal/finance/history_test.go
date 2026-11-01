package finance

import (
	"database/sql"
	"testing"
	"time"

	"github.com/base48/member-portal/internal/db"
)

func fee(user int64, month string, amount float64) db.ListMonthlyFeesByUserRow {
	return db.ListMonthlyFeesByUserRow{UserID: user, Month: month, FeeCount: 1, Amount: amount}
}

func paid(user int64, month string, amount float64) db.ListMonthlyMemberPaymentsByUserRow {
	return db.ListMonthlyMemberPaymentsByUserRow{UserID: sql.NullInt64{Int64: user, Valid: true}, Month: month, PaymentCount: 1, Amount: amount}
}

func held(account, day string, balance float64) db.BankBalance {
	return db.BankBalance{Account: account, Day: day, Balance: balance}
}

func TestHistoryCountsDebtOfThoseChargedThatMonth(t *testing.T) {
	fees := []db.ListMonthlyFeesByUserRow{
		fee(1, "2026-01", 1000), fee(1, "2026-02", 1000), fee(1, "2026-03", 1000),
		fee(2, "2026-01", 500),  // leaves after January, still owing
		fee(1, "2026-04", 1000), // after last: left out
	}
	payments := []db.ListMonthlyMemberPaymentsByUserRow{
		paid(1, "2025-12", 1000), // paid ahead
		paid(1, "2026-02", 500),
	}
	got := History(fees, payments, nil, day("2026-03-15"))

	type row struct {
		month         string
		charged, paid float64
		debt          float64
		debtors       int
	}
	want := []row{
		{"2025-12", 0, 1000, 0, 0},
		{"2026-01", 1500, 0, 500, 1},   // member 2 owes; member 1 used the advance
		{"2026-02", 1000, 500, 500, 1}, // member 2 is no longer charged
		{"2026-03", 1000, 0, 1500, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("History = %d months, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Month != w.month || g.Charged != w.charged || g.Paid != w.paid || g.Debt != w.debt || g.Debtors != w.debtors {
			t.Errorf("month %d = %+v, want %+v", i, g, w)
		}
		if g.Balance != nil {
			t.Errorf("%s balance = %v without any statement", g.Month, *g.Balance)
		}
	}
}

func TestHistoryAddsUpTheAccounts(t *testing.T) {
	fees := []db.ListMonthlyFeesByUserRow{fee(1, "2025-11", 1000)}
	balances := []db.BankBalance{ // by day, as ListBankBalances returns them
		held("old", "2010-01-01", 0), // opening of the full statement
		held("old", "2025-12-02", 300),
		held("new", "2026-01-05", 50),
		held("old", "2026-01-10", 640), // members still pay to the old account
		held("old", "2026-01-31", 0),   // closed, everything moved
		held("new", "2026-01-31", 390),
		held("new", "2026-02-01", 10), // rent
		held("new", "2026-02-20", 60),
	}
	got := History(fees, nil, balances, day("2026-03-10"))

	want := map[string]Balance{
		"2025-11": {Avg: 0, End: 0, Low: 0, High: 0},
		"2025-12": {Avg: 300 * 30.0 / 31, End: 300, Low: 0, High: 300},
		// 300 for 4 days, 350 for 5, 690 for 21, 390 for the 31st.
		"2026-01": {Avg: (300*4 + 350*5 + 690*21 + 390) / 31.0, End: 390, Low: 300, High: 690},
		"2026-02": {Avg: (10*19 + 60*9) / 28.0, End: 60, Low: 10, High: 60},
		"2026-03": {Avg: 60, End: 60, Low: 60, High: 60}, // nothing moved, until today
	}
	if len(got) != len(want) {
		t.Fatalf("History = %d months, want %d", len(got), len(want))
	}
	for _, m := range got {
		if m.Balance == nil || *m.Balance != want[m.Month] {
			t.Errorf("%s balance = %+v, want %+v", m.Month, m.Balance, want[m.Month])
		}
	}
}

func TestHistoryHasNoBalanceBeforeTheFirstStatement(t *testing.T) {
	fees := []db.ListMonthlyFeesByUserRow{fee(1, "2026-01", 1000)}
	got := History(fees, nil, []db.BankBalance{held("new", "2026-02-10", 500)}, day("2026-02-20"))
	if len(got) != 2 || got[0].Balance != nil {
		t.Fatalf("January balance = %+v, want none", got[0].Balance)
	}
	// The average counts the days from the first one known.
	if b := got[1].Balance; b == nil || *b != (Balance{Avg: 500, End: 500, Low: 500, High: 500}) {
		t.Fatalf("February balance = %+v", b)
	}
}

func TestHistorySkipsWhatIsNotAMonth(t *testing.T) {
	fees := []db.ListMonthlyFeesByUserRow{fee(1, "", 1000), fee(1, "2026-02", 1000)}
	got := History(fees, nil, nil, day("2026-02-01"))
	if len(got) != 1 || got[0].Charged != 1000 {
		t.Fatalf("History = %+v", got)
	}
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}
