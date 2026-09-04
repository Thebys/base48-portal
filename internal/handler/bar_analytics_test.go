package handler

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/base48/member-portal/internal/db"
)

// wall builds a transaction timestamp the way the sync actually stores them:
// revbank logs Prague wall clock ("2026-03-07_04:09:09") and parseFlexibleTime
// reads it with a zoneless layout, so the value lands in the database as that
// same wall clock wearing a UTC label. Tests therefore construct times in UTC
// meaning the local hour, not the instant.
func wall(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func tx(id, user string, cents int64, desc string, at time.Time) db.RevbankTransaction {
	return db.RevbankTransaction{
		TransactionID: id,
		Username:      user,
		UserID:        sql.NullInt64{Int64: 1, Valid: true},
		AmountCents:   cents,
		Description:   desc,
		CreatedAt:     at,
	}
}

func TestClassifyBarTxDropsUndoPairs(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-03-10_21:11:28_T66_freedom", "freedom", -15000, "bramborky [5x 30.00]", wall(2026, 3, 10, 20, 11)),
		tx("2026-03-10_21:12:51_T67_freedom", "freedom", 18000, "Undo 66", wall(2026, 3, 10, 20, 12)),
		tx("2026-03-10_21:20:00_T68_freedom", "freedom", -3000, "semtex", wall(2026, 3, 10, 20, 20)),
	}

	got := classifyBarTx(rows)
	if len(got) != 3 {
		t.Fatalf("classifyBarTx returned %d rows, want 3", len(got))
	}
	if got[0].Kind != barVoid {
		t.Errorf("reversed original: kind = %v, want barVoid", got[0].Kind)
	}
	if got[1].Kind != barVoid {
		t.Errorf("undo row: kind = %v, want barVoid", got[1].Kind)
	}
	if got[2].Kind != barPurchase {
		t.Errorf("untouched purchase: kind = %v, want barPurchase", got[2].Kind)
	}
}

func TestClassifyBarTxParsesMultiBuy(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-05-01_12:00:00_T1_a", "a", -15000, "bramborky [5x 30.00]", wall(2026, 5, 1, 10, 0)),
		tx("2026-05-01_12:01:00_T2_a", "a", -3000, "Kozel 10 plech", wall(2026, 5, 1, 10, 1)),
	}

	got := classifyBarTx(rows)
	if got[0].Qty != 5 {
		t.Errorf("multi-buy Qty = %d, want 5", got[0].Qty)
	}
	if got[0].Unit != 3000 {
		t.Errorf("multi-buy Unit = %d, want 3000", got[0].Unit)
	}
	if got[0].Product != "bramborky" {
		t.Errorf("multi-buy Product = %q, want %q", got[0].Product, "bramborky")
	}
	if got[1].Qty != 1 || got[1].Unit != 3000 {
		t.Errorf("single buy: Qty=%d Unit=%d, want 1/3000", got[1].Qty, got[1].Unit)
	}
}

func TestClassifyBarTxKinds(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-05-01_12:00:00_T1_a", "a", 10000, "Deposit (Cash)", wall(2026, 5, 1, 10, 0)),
		tx("2026-05-01_12:01:00_T2_a", "a", 5000, "Received from vega (kebab)", wall(2026, 5, 1, 10, 1)),
		tx("2026-05-01_12:02:00_T3_a", "a", 2000, "Reimbursement (bathroom sensor, approval: thebys)", wall(2026, 5, 1, 10, 2)),
		tx("2026-05-01_12:03:00_T4_a", "a", -3000, "Ginger beer", wall(2026, 5, 1, 10, 3)),
	}

	want := []barKind{barDeposit, barTransfer, barTransfer, barPurchase}
	got := classifyBarTx(rows)
	for i, w := range want {
		if got[i].Kind != w {
			t.Errorf("row %d: kind = %v, want %v", i, got[i].Kind, w)
		}
	}
	// Amounts are stored signed but carried as magnitudes.
	if got[3].Amount != 3000 {
		t.Errorf("purchase Amount = %d, want 3000 (magnitude)", got[3].Amount)
	}
}

// The regression this guards: a Friday 23:30 sale must stay on Friday. The
// stored value is already Prague wall clock, so converting it into Prague a
// second time would push it to Saturday 01:30 and move a whole Friday night of
// bar traffic onto the wrong day, week and — at a month end — the wrong month.
func TestBarAnalyticsBucketsWallClock(t *testing.T) {
	// 2026-07-03 is a Friday.
	rows := []db.RevbankTransaction{
		tx("2026-07-03_23:30:00_T1_a", "a", -3000, "Ginger beer", wall(2026, 7, 3, 23, 30)),
	}
	a := computeBarAnalytics(rows, nil, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	// Heat rows are Monday-first, so Friday is index 4 and Saturday index 5.
	if got := a.Heat[4].Cells[23].Count; got != 1 {
		t.Errorf("Friday 23:00 count = %d, want 1", got)
	}
	if got := a.Heat[5].Count; got != 0 {
		t.Errorf("Saturday should be empty, got %d", got)
	}
}

// time.Now() is a real instant and has to be folded into the same wall-clock
// space before it can be compared with a stored timestamp.
func TestBarWallNow(t *testing.T) {
	if barLoc().String() != "Europe/Prague" {
		t.Skip("tzdata unavailable")
	}
	// 22:30 UTC on a summer day is 00:30 the next day in Prague.
	got := barWallNow(time.Date(2026, 7, 3, 22, 30, 0, 0, time.UTC))
	want := time.Date(2026, 7, 4, 0, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("barWallNow = %v, want %v", got, want)
	}
}

func TestBarAnalyticsVisitsAndTotals(t *testing.T) {
	base := wall(2026, 5, 1, 10, 0)
	rows := []db.RevbankTransaction{
		// One visit: three items inside the 3h window.
		tx("2026-05-01_12:00:00_T1_a", "a", -3000, "Kozel 10 plech", base),
		tx("2026-05-01_12:30:00_T2_a", "a", -1500, "Horalky", base.Add(30*time.Minute)),
		tx("2026-05-01_14:00:00_T3_a", "a", -3000, "Ginger beer", base.Add(2*time.Hour)),
		// A separate visit the next day.
		tx("2026-05-02_12:00:00_T4_a", "a", -2000, "Milena", base.Add(26*time.Hour)),
		// Another person, and a deposit that must not count as revenue.
		tx("2026-05-01_12:00:00_T5_b", "b", -3000, "Ginger beer", base),
		tx("2026-05-01_12:05:00_T6_b", "b", 20000, "Deposit (Cash)", base.Add(5*time.Minute)),
	}
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	a := computeBarAnalytics(rows, nil, now)

	if !a.HasData {
		t.Fatal("HasData = false")
	}
	if len(a.Months) != 1 {
		t.Fatalf("got %d months, want 1", len(a.Months))
	}
	m := a.Months[0]
	if m.Revenue != 12500 {
		t.Errorf("month revenue = %d, want 12500 (deposit excluded)", m.Revenue)
	}
	if m.Deposits != 20000 {
		t.Errorf("month deposits = %d, want 20000", m.Deposits)
	}
	if m.Visits != 3 {
		t.Errorf("visits = %d, want 3 (a×2, b×1)", m.Visits)
	}
	if m.Buyers != 2 {
		t.Errorf("buyers = %d, want 2", m.Buyers)
	}
}

func TestBarAnalyticsProductsAggregateUnits(t *testing.T) {
	base := wall(2026, 5, 1, 10, 0)
	rows := []db.RevbankTransaction{
		tx("2026-05-01_12:00:00_T1_a", "a", -6000, "Kozel 10 plech [2x 30.00]", base),
		tx("2026-05-01_12:01:00_T2_a", "a", -3000, "kozel 10 plech", base.Add(time.Minute)),
		tx("2026-05-01_12:02:00_T3_a", "a", -1500, "Horalky", base.Add(2*time.Minute)),
	}
	a := computeBarAnalytics(rows, nil, time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))

	if len(a.Products) != 2 {
		t.Fatalf("got %d products, want 2 (case-insensitive grouping)", len(a.Products))
	}
	top := a.Products[0]
	if top.Units != 3 {
		t.Errorf("Kozel units = %d, want 3 (2 from the multi-buy + 1)", top.Units)
	}
	if top.Revenue != 9000 {
		t.Errorf("Kozel revenue = %d, want 9000", top.Revenue)
	}
	if top.Unit != 3000 {
		t.Errorf("Kozel unit price = %d, want 3000", top.Unit)
	}
}

func TestBarAnalyticsBalancesSplitCreditAndDebt(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-05-01_12:00:00_T1_a", "a", -3000, "Ginger beer", wall(2026, 5, 1, 10, 0)),
	}
	accounts := []db.RevbankAccount{
		{Username: "a", BalanceCents: -15900},
		{Username: "b", BalanceCents: 182800},
		{Username: "c", BalanceCents: 0},
	}
	a := computeBarAnalytics(rows, accounts, time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))

	if a.CreditHeld != 182800 {
		t.Errorf("CreditHeld = %d, want 182800", a.CreditHeld)
	}
	if a.DebtTotal != 15900 {
		t.Errorf("DebtTotal = %d, want 15900 (positive magnitude)", a.DebtTotal)
	}
	if len(a.Debtors) != 1 || a.Debtors[0].Username != "a" {
		t.Errorf("Debtors = %+v, want just 'a'", a.Debtors)
	}
}

func TestBarAnalyticsEmpty(t *testing.T) {
	a := computeBarAnalytics(nil, nil, time.Now())
	if a.HasData {
		t.Error("HasData = true for an empty history")
	}
	if len(a.Stats) != 0 || len(a.Heat) != 0 {
		t.Error("empty history should produce no stats and no heatmap")
	}
}

func TestBarCategoryOf(t *testing.T) {
	cases := map[string]string{
		"kozel 10 plech":            "Pivo a cider",
		"radecek <3":                "Pivo a cider",
		"fuchsapfel cider":          "Pivo a cider",
		"monster energy - viking":   "Energy",
		"big shock gold":            "Energy",
		"cristaline pitna voda":     "Nealko",
		"orangina original 0.5 l":   "Nealko",
		"horalky peanut butter":     "Snack",
		"quinos chips - creme":      "Snack",
		"neznama vec bez klicoveho": "Ostatní",
	}
	for key, want := range cases {
		if got, _ := barCategoryOf(key); got != want {
			t.Errorf("barCategoryOf(%q) = %q, want %q", key, got, want)
		}
	}
}

// revbank restarts reset the checkout counter, so the same "_TN_" turns up
// months apart. An undo must reverse the checkout it followed, not an unrelated
// row that happens to share a recycled number.
func TestClassifyBarTxUndoPicksNearestPrecedingCheckout(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-05-23_14:31:41_T1733_vaclav", "vaclav", -3000, "Birell", wall(2026, 5, 23, 14, 31)),
		tx("2026-05-24_18:00:01_T1733_vanicka", "vanicka", -3000, "Zlatopramen", wall(2026, 5, 24, 18, 0)),
		tx("2026-05-24_18:00:30_T1734_vanicka", "vanicka", 3000, "Undo 1733", wall(2026, 5, 24, 18, 0)),
	}
	got := classifyBarTx(rows)

	if got[0].Kind != barPurchase {
		t.Errorf("May 23 row: kind = %v, want barPurchase (a recycled number must not void it)", got[0].Kind)
	}
	if got[1].Kind != barVoid {
		t.Errorf("May 24 row: kind = %v, want barVoid", got[1].Kind)
	}
}

// A transfer writes both sides of the move under one checkout number, with an
// identical timestamp. Undoing it has to void both, not whichever the map kept.
func TestClassifyBarTxUndoVoidsWholeCheckout(t *testing.T) {
	at := wall(2026, 3, 20, 21, 32)
	rows := []db.RevbankTransaction{
		tx("2026-03-20_21:32:40_T371_vega", "vega", -5000, "Give to skyler", at),
		tx("2026-03-20_21:32:40_T371_skyler", "skyler", 5000, "Received from vega", at),
		tx("2026-03-20_21:33:10_T372_vega", "vega", 5000, "Undo 371", at.Add(30*time.Second)),
	}
	got := classifyBarTx(rows)

	for i := 0; i < 2; i++ {
		if got[i].Kind != barVoid {
			t.Errorf("row %d: kind = %v, want barVoid (both sides of the checkout)", i, got[i].Kind)
		}
	}
}

// "[3x 8.35]" states the unit price; deriving it from the total truncates to
// 8.34 and then reads as a price change against the same item sold singly.
func TestClassifyBarTxUsesStatedUnitPrice(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-05-01_12:00:00_T1_a", "a", -2504, "Fidorka [3x 8.35]", wall(2026, 5, 1, 12, 0)),
		tx("2026-05-01_12:01:00_T2_a", "a", -835, "Fidorka", wall(2026, 5, 1, 12, 1)),
	}
	got := classifyBarTx(rows)
	if got[0].Unit != 835 {
		t.Errorf("multi-buy unit = %d, want 835 (the price the description states)", got[0].Unit)
	}

	a := computeBarAnalytics(rows, nil, time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))
	if len(a.Products) != 1 {
		t.Fatalf("got %d products, want 1", len(a.Products))
	}
	if a.Products[0].PriceNote != "" {
		t.Errorf("PriceNote = %q, want empty — the price never moved", a.Products[0].PriceNote)
	}
}

// A quiet week still owns a slot: dropping it would put two columns far apart
// side by side under a caption promising consecutive weeks.
func TestBarAnalyticsFillsQuietPeriods(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-05-04_12:00:00_T1_a", "a", -3000, "Ginger beer", wall(2026, 5, 4, 12, 0)),
		tx("2026-07-06_12:00:00_T2_a", "a", -3000, "Ginger beer", wall(2026, 7, 6, 12, 0)),
	}
	a := computeBarAnalytics(rows, nil, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	if len(a.Columns) != 10 {
		t.Errorf("got %d weekly columns, want 10 (4 May → 6 July inclusive)", len(a.Columns))
	}
	if len(a.Months) != 3 {
		t.Errorf("got %d months, want 3 (May, June, July)", len(a.Months))
	}
	if len(a.Months) == 3 && a.Months[1].Revenue != 0 {
		t.Errorf("June revenue = %d, want 0 but present", a.Months[1].Revenue)
	}
	if len(a.CohortMonths) != 3 {
		t.Errorf("got %d cohort columns, want 3", len(a.CohortMonths))
	}
}

// With fewer buyers than the headline N, the page must not claim 0 %.
func TestBarAnalyticsConcentrationWithFewBuyers(t *testing.T) {
	rows := []db.RevbankTransaction{
		tx("2026-05-01_12:00:00_T1_a", "a", -3000, "Ginger beer", wall(2026, 5, 1, 12, 0)),
		tx("2026-05-01_12:01:00_T2_b", "b", -1000, "Voda", wall(2026, 5, 1, 12, 1)),
	}
	a := computeBarAnalytics(rows, nil, time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))

	if a.Top5Pct != 100 || a.Top10Pct != 100 {
		t.Errorf("Top5/Top10 = %.0f/%.0f, want 100/100 — two buyers are all of them", a.Top5Pct, a.Top10Pct)
	}
	if a.TopN5 != 2 || a.TopN10 != 2 {
		t.Errorf("TopN5/TopN10 = %d/%d, want 2/2", a.TopN5, a.TopN10)
	}
}

// The cohort cells carry readable numbers, so their ramp must stay inside the
// steps that clear 4.5:1 against the ink used on them.
func TestBarAnalyticsCohortRampStaysReadable(t *testing.T) {
	var rows []db.RevbankTransaction
	for i := 0; i < 8; i++ {
		rows = append(rows, tx(
			fmt.Sprintf("2026-05-01_12:0%d:00_T%d_u%d", i, i, i),
			fmt.Sprintf("u%d", i), -3000, "Ginger beer", wall(2026, 5, 1, 12, i)))
	}
	a := computeBarAnalytics(rows, nil, time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC))

	for _, c := range a.Cohorts {
		for _, cell := range c.Cells {
			if cell.Step < 0 || cell.Step > 5 {
				t.Errorf("cohort step %d outside the AA-safe 0..5 ramp", cell.Step)
			}
		}
	}
}
