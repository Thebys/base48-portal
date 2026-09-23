package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
)

// runBarDebtEmails queues bar debt reminders for accepted members whose kiosk
// debt is above the admin-set threshold. Everything — on/off,
// threshold, interval, outbox delay — comes from the admin settings page.
// Safe to run repeatedly: a member reminded within the interval is skipped.
//
// Usage:
//
//	portal-cron bar-debt-emails
func runBarDebtEmails(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	settings := email.LoadBarDebtSettings(ctx, queries)
	if !settings.Enabled {
		log.Println("Bar debt emails are switched off in admin settings, nothing to do")
		return 0
	}

	log.Printf("Checking bar balances (debt above %d Kč, interval %d days, delay %dh)...",
		settings.ThresholdCZK, settings.IntervalDays, settings.DelayHours)

	emailClient := email.New(cfg, queries, nil)
	res, err := emailClient.QueueBarDebtReminders(ctx, settings)
	if err != nil {
		log.Printf("Bar debt emails failed: %v", err)
		queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "cron",
			Level:     "error",
			Message:   "Bar debt emails failed: " + err.Error(),
		})
		return 1
	}

	log.Printf("  Debt above threshold: %d", res.Debtors)
	log.Printf("  Emails queued: %d", res.Queued)
	log.Printf("  Skipped (reminded recently): %d", res.Skipped)
	log.Printf("  Errors: %d", res.Errors)

	level := "success"
	if res.Errors > 0 {
		level = "warning"
	}
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "cron",
		Level:     level,
		UserID:    sql.NullInt64{},
		Message:   fmt.Sprintf("Bar debt emails: %d queued, %d skipped, %d errors", res.Queued, res.Skipped, res.Errors),
		Metadata: sql.NullString{
			String: fmt.Sprintf(`{"debtors":%d,"queued":%d,"skipped":%d,"errors":%d,"threshold_czk":%d}`,
				res.Debtors, res.Queued, res.Skipped, res.Errors, settings.ThresholdCZK),
			Valid: true,
		},
	})

	if res.Errors > 0 {
		return 1
	}
	return 0
}
