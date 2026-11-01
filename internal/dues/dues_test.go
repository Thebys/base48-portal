package dues

import (
	"testing"
	"time"
)

var nov = Month{2026, time.November}

func TestFeeAmount(t *testing.T) {
	for _, tc := range []struct {
		actual, level, want string
		fellBack, err       bool
	}{
		{"1500", "1000", "1500", false, false},
		{"0", "1000", "1000", false, false},
		{"", "1000", "1000", false, false},
		{"NaN", "1000", "1000", true, false},
		{"+Inf", "1000", "1000", true, false},
		{"1000abc", "1000", "1000", true, false},
		{"0", "0", "0", false, false},
		{"0", "NaN", "", false, true},
	} {
		got, fellBack, err := FeeAmount(tc.actual, tc.level)
		if got != tc.want || fellBack != tc.fellBack || (err != nil) != tc.err {
			t.Errorf("FeeAmount(%q, %q) = %q, %t, %v", tc.actual, tc.level, got, fellBack, err)
		}
	}
	if fee := MonthlyFee("NaN", "600"); fee != 600 {
		t.Errorf("MonthlyFee of a junk amount = %v, want the level's 600", fee)
	}
}

func TestMonth(t *testing.T) {
	if got := nov.Add(2).String(); got != "2027-01" {
		t.Errorf("Nov + 2 = %s", got)
	}
	if got := nov.Add(-11).String(); got != "2025-12" {
		t.Errorf("Nov - 11 = %s", got)
	}
	if m, err := ParseMonth("2026-11-01"); err != nil || m != nov {
		t.Errorf("ParseMonth = %v, %v", m, err)
	}
	if _, err := ParseMonth(""); err == nil {
		t.Error("an empty month parsed")
	}
	if !nov.Before(nov.Add(1)) || nov.Before(nov) {
		t.Error("Before")
	}
}

func TestStanding(t *testing.T) {
	for _, tc := range []struct {
		balance     float64
		paidThrough string
		debt, short float64
	}{
		{0, "2026-11", 0, 1000},        // paid exactly for November
		{500, "2026-11", 0, 500},       // and half of December
		{1000, "2026-12", 0, 1000},     // December too
		{-300, "2026-10", 300, 300},    // November 300 short
		{-1000, "2026-10", 1000, 1000}, // November unpaid
		{-2400, "2026-08", 2400, 400},  // since September, and 400 of August
	} {
		s := Standing{Fee: 1000, Balance: tc.balance, Anchor: nov}
		if got := s.PaidThrough().String(); got != tc.paidThrough {
			t.Errorf("balance %v: paid through %s, want %s", tc.balance, got, tc.paidThrough)
		}
		if s.Debt() != tc.debt || s.Short() != tc.short {
			t.Errorf("balance %v: debt %v short %v, want %v %v", tc.balance, s.Debt(), s.Short(), tc.debt, tc.short)
		}
	}
}

// The suggestion is the debt plus whole months, and paying it covers exactly
// those months ahead; a credit stays on top.
func TestSuggest(t *testing.T) {
	for _, tc := range []struct {
		balance float64
		months  int
		amount  float64
		covers  string
	}{
		{0, 3, 3000, "2027-02"},
		{500, 3, 3000, "2027-02"},
		{-300, 3, 3300, "2027-02"},
		{-2400, 3, 5400, "2027-02"},
		{-2400, 1, 3400, "2026-12"},
		{2000, 12, 12000, "2028-01"},
	} {
		s := Standing{Fee: 1000, Balance: tc.balance, Anchor: nov}
		if got := s.Suggest(tc.months); got != tc.amount {
			t.Errorf("balance %v, %d months: suggest %v, want %v", tc.balance, tc.months, got, tc.amount)
		}
		if got := s.Covers(s.Suggest(tc.months)).String(); got != tc.covers {
			t.Errorf("balance %v, %d months: covers %s, want %s", tc.balance, tc.months, got, tc.covers)
		}
	}
}

// The debt tiers are those of the old DebtTier: -fee and -2·fee inclusive.
// The prepaid reminder covers what lies between: anyone not paid beyond this
// month, including those a little short.
func TestClassify(t *testing.T) {
	on := Policy{Prepaid: true, MonthsAhead: 1}
	off := Policy{}
	for _, tc := range []struct {
		balance float64
		p       Policy
		want    string
	}{
		{-2000, on, DebtWarning},
		{-1999, on, NegativeBalance},
		{-1000, on, NegativeBalance},
		{-999, on, PrepaidEnding},
		{-999, off, None},
		{0, on, PrepaidEnding},
		{999, on, PrepaidEnding},
		{1000, on, None},
		{1000, Policy{Prepaid: true, MonthsAhead: 2}, PrepaidEnding},
		{2000, Policy{Prepaid: true, MonthsAhead: 2}, None},
		{0, off, None},
	} {
		s := Standing{Fee: 1000, Balance: tc.balance, Anchor: nov}
		if got := Classify(s, nov, tc.p); got != tc.want {
			t.Errorf("balance %v %+v: %q, want %q", tc.balance, tc.p, got, tc.want)
		}
	}
	if got := Classify(Standing{Fee: 0, Balance: -5000, Anchor: nov}, nov, on); got != None {
		t.Errorf("a free membership got %q", got)
	}
	if got := Classify(Standing{Fee: 1000, Balance: -5000}, nov, on); got != None {
		t.Errorf("a member never charged got %q", got)
	}
}

// Before the run on the 1st and right after it, a member stands the same:
// the anchor moves on by a month and the balance drops by a fee.
func TestStandingIsTheSameAcrossTheRun(t *testing.T) {
	before := Standing{Fee: 1000, Balance: 500, Anchor: nov}
	after := before.AfterCharge(nov.Add(1))
	if before.PaidThrough() != after.PaidThrough() {
		t.Errorf("paid through %s before the run, %s after", before.PaidThrough(), after.PaidThrough())
	}
	if again := after.AfterCharge(nov.Add(1)); again != after {
		t.Error("charging the same month twice moved the balance")
	}
	// In December, without paying, the November-only payer is short.
	if got := Classify(after, nov.Add(1), Policy{Prepaid: true, MonthsAhead: 1}); got != PrepaidEnding {
		t.Errorf("after the December run: %q", got)
	}
}

func TestAnchor(t *testing.T) {
	if got := Anchor("2026-12", nov); got != nov.Add(1) {
		t.Errorf("a fee entered ahead: anchor %s", got)
	}
	if got := Anchor("2026-03", nov); got != nov {
		t.Errorf("not charged since March (joined again): anchor %s, want the run month", got)
	}
	if got := Anchor("", nov); got != nov {
		t.Errorf("never charged: anchor %s", got)
	}
}
