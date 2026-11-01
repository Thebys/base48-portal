package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
)

// The monthly run charges the membership fees and then queues the membership
// reminders. The month each of them last ran is kept in settings, as
// "2006-01", so a daemon restarted on the 1st does not run them again, and one
// that was down on the 1st catches up within the first week. Fees and
// reminders are recorded apart: reminders that could not be queued (no payment
// account known yet, say) are tried again without charging anybody twice.
const (
	settingFeesMonth      = dues.SettingFeesMonth
	settingRemindersMonth = dues.SettingRemindersMonth

	// catchUpDays is the last day of the month a missed run is still made up.
	// Later, fees would land on members who joined or left meanwhile, and
	// reminders would quote a month that is half over: an admin decides.
	catchUpDays = 7
)

// monthlyPlan says what the daemon should run now.
type monthlyPlan struct {
	fees      bool
	reminders bool
	// missed is set when a month went by without fees; nothing is made up
	// automatically, it is for an admin to see.
	missed string
}

// monthlyDue decides the monthly run from the local time and the months fees
// and reminders last ran ("" when never recorded). Without a record it keeps
// to the old rule, fees on the 1st, until the first run writes one.
func monthlyDue(now time.Time, feesMonth, remindersMonth string) monthlyPlan {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	month := first.Format("2006-01")
	previous := first.AddDate(0, -1, 0).Format("2006-01")
	early := now.Day() <= catchUpDays

	var plan monthlyPlan
	switch {
	case feesMonth == month:
	case feesMonth == "":
		plan.fees = now.Day() == 1
	case feesMonth == previous && early:
		plan.fees = true
	case feesMonth == previous:
		plan.missed = fmt.Sprintf("předpis za %s neproběhl do %d. dne v měsíci", month, catchUpDays)
	default:
		plan.missed = fmt.Sprintf("předpis naposledy proběhl za %s", feesMonth)
	}

	charged := feesMonth == month || plan.fees
	plan.reminders = charged && remindersMonth != month && early
	return plan
}

// monthOf reads a month recorded in settings, "" when there is none.
func monthOf(ctx context.Context, queries *db.Queries, key string) (string, error) {
	setting, err := queries.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}
	return setting.Value, nil
}

// recordMonth stores the month of now as done for key.
func recordMonth(ctx context.Context, queries *db.Queries, key string, now time.Time) error {
	if _, err := queries.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: now.Format("2006-01")}); err != nil {
		return fmt.Errorf("record %s: %w", key, err)
	}
	return nil
}

// monthStartUTC is the start of now's local month in UTC, written the way
// SQLite's CURRENT_TIMESTAMP writes created_at.
func monthStartUTC(now time.Time) string {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return first.UTC().Format("2006-01-02 15:04:05")
}
