package email

import (
	"testing"
	"time"
)

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
	if !now.alwaysSchedule {
		t.Error("Scheduled(0) must still park the email in the outbox, not send it inline")
	}
	if now.DefaultDelay != 0 {
		t.Errorf("Scheduled(0).DefaultDelay = %v, want 0", now.DefaultDelay)
	}

	later := base.Scheduled(72 * time.Hour)
	if later.DefaultDelay != 72*time.Hour {
		t.Errorf("Scheduled(72h).DefaultDelay = %v, want 72h", later.DefaultDelay)
	}

	// The shared client is reused by every other caller in the process, so a
	// per-request timer must never leak back into it.
	if base.DefaultDelay != 24*time.Hour || base.alwaysSchedule {
		t.Errorf("Scheduled mutated the receiver: delay=%v alwaysSchedule=%v",
			base.DefaultDelay, base.alwaysSchedule)
	}
}
