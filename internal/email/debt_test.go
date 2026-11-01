package email

import (
	"testing"
	"time"
)

func TestMonthlyFee(t *testing.T) {
	tests := []struct {
		name                      string
		actualAmount, levelAmount string
		want                      float64
	}{
		{"individual override wins", "750", "1000", 750},
		{"zero override falls back to the level", "0", "1000", 1000},
		{"empty override falls back to the level", "", "1000", 1000},
		{"decimal amount", "500.5", "1000", 500.5},
		{"free level", "0", "0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MonthlyFee(tt.actualAmount, tt.levelAmount); got != tt.want {
				t.Errorf("MonthlyFee(%q, %q) = %v, want %v",
					tt.actualAmount, tt.levelAmount, got, tt.want)
			}
		})
	}
}

func TestDebtTier(t *testing.T) {
	tests := []struct {
		name       string
		balance    float64
		monthlyFee float64
		want       string
	}{
		{"in credit", 500, 1000, ""},
		{"exactly zero", 0, 1000, ""},
		{"owes less than one fee", -999, 1000, ""},
		{"owes exactly one fee", -1000, 1000, TierNegativeBalance},
		{"owes more than one fee", -1500, 1000, TierNegativeBalance},
		{"owes just under two fees", -1999, 1000, TierNegativeBalance},
		{"owes exactly two fees", -2000, 1000, TierDebtWarning},
		{"owes far more than two fees", -9000, 1000, TierDebtWarning},
		// Honorary/free memberships have no fee to fall behind on, so no
		// negative balance of theirs may ever trigger a reminder.
		{"zero fee never warns", -5000, 0, ""},
		{"negative fee never warns", -5000, -100, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DebtTier(tt.balance, tt.monthlyFee); got != tt.want {
				t.Errorf("DebtTier(%v, %v) = %q, want %q", tt.balance, tt.monthlyFee, got, tt.want)
			}
		})
	}
}

// The tier names double as the content-block keys and, with .html appended, as
// the outbox template_name the anti-spam lookup matches on. If they drift apart,
// reminders silently stop being deduplicated.
func TestTemplateFileForTier(t *testing.T) {
	tests := []struct{ tier, want string }{
		{TierDebtWarning, "debt_warning.html"},
		{TierNegativeBalance, "negative_balance.html"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := TemplateFileForTier(tt.tier); got != tt.want {
			t.Errorf("TemplateFileForTier(%q) = %q, want %q", tt.tier, got, tt.want)
		}
	}
	for _, tier := range []string{TierDebtWarning, TierNegativeBalance} {
		if _, ok := defaultContentBlocks[tier]; !ok {
			t.Errorf("tier %q has no Czech content blocks", tier)
		}
		if _, ok := defaultContentBlocksEN[tier]; !ok {
			t.Errorf("tier %q has no English content blocks", tier)
		}
	}
}

func TestScheduledIsACopy(t *testing.T) {
	base := &Client{DefaultDelay: 24 * time.Hour}

	now := base.Scheduled(0)
	if now.DefaultDelay != 0 {
		t.Errorf("Scheduled(0).DefaultDelay = %v, want 0", now.DefaultDelay)
	}

	later := base.Scheduled(72 * time.Hour)
	if later.DefaultDelay != 72*time.Hour {
		t.Errorf("Scheduled(72h).DefaultDelay = %v, want 72h", later.DefaultDelay)
	}

	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.Local)
	if fixed := base.At(at); !fixed.dueAt.Equal(at) {
		t.Errorf("At(%v).dueAt = %v", at, fixed.dueAt)
	}

	// The shared client is reused by every other caller in the process, so a
	// per-request timer must never leak back into it.
	if base.DefaultDelay != 24*time.Hour || !base.dueAt.IsZero() {
		t.Errorf("Scheduled or At mutated the receiver: delay=%v dueAt=%v", base.DefaultDelay, base.dueAt)
	}
}

// Reminders queued by the run on the 1st leave on the 4th at 10:00 local
// time, whichever offset the zone has that day.
func TestLocalHourAfter(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	local := time.Local
	time.Local = prague
	t.Cleanup(func() { time.Local = local })

	for _, tc := range []struct {
		queued time.Time
		want   string // UTC
	}{
		{time.Date(2026, 11, 1, 0, 1, 0, 0, prague), "2026-11-04 09:00"},  // CET
		{time.Date(2026, 4, 1, 0, 1, 0, 0, prague), "2026-04-04 08:00"},   // CEST
		{time.Date(2026, 3, 1, 15, 30, 0, 0, prague), "2026-03-05 09:00"}, // after 10:00: next day
		{time.Date(2026, 11, 28, 10, 0, 0, 0, prague), "2026-12-01 09:00"},
	} {
		got := LocalHourAfter(tc.queued.Add(72*time.Hour), 10).UTC().Format("2006-01-02 15:04")
		if got != tc.want {
			t.Errorf("queued %v: due %s UTC, want %s", tc.queued, got, tc.want)
		}
	}
}
