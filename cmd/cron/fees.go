package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
)

// runFees creates this month's membership fee for every accepted member and
// records the month as charged (see monthlyDue). It sends no email: the
// membership reminders are runDebtEmails' job, which the daemon runs right
// after this.
//
// Usage:
//
//	portal-cron fees
func runFees(ctx context.Context, _ *config.Config, queries *db.Queries) int {
	errors, err := chargeMonthlyFees(ctx, queries, time.Now())
	if err != nil {
		log.Printf("✗ Fees not created: %v", err)
		return 1
	}
	if errors > 0 {
		log.Println("Job completed with errors")
		return 1
	}
	log.Println("✓ Job completed successfully")
	return 0
}

// chargeMonthlyFees creates the fees for the month of now. Members who already
// have one for it are skipped, so a second run changes nothing. The error is
// set only when the run as a whole could not happen; members it failed for are
// counted and logged, and the month is recorded as charged all the same, as
// another run would fail for them the same way.
func chargeMonthlyFees(ctx context.Context, queries *db.Queries, now time.Time) (errors int, err error) {
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	log.Printf("Creating fees for period: %s", periodStart.Format("2006-01"))

	users, err := queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		return 0, fmt.Errorf("list members: %w", err)
	}

	log.Printf("Processing %d accepted members...", len(users))

	created := 0
	skipped := 0

	for _, user := range users {
		// Zkontrolujeme, jestli už fee pro tento měsíc neexistuje
		existingFee, err := queries.GetFeeByUserAndPeriod(ctx, db.GetFeeByUserAndPeriodParams{
			UserID:      user.ID,
			PeriodStart: periodStart,
		})

		if err == nil && existingFee.ID > 0 {
			log.Printf("  ⊘ Skipping %s - fee already exists for %s", user.Email, periodStart.Format("2006-01"))
			skipped++
			continue
		}

		// The member's own amount, else the level's (dues.FeeAmount). A junk
		// amount such as "NaN", which the profile used to let through, would
		// otherwise be summed by SQLite as 0: the member would pay nothing.
		feeAmount, fellBack, err := dues.FeeAmount(user.LevelActualAmount, user.LevelAmount)
		if err != nil {
			log.Printf("  ✗ No fee for %s: %v", user.Email, err)
			errors++
			continue
		}
		if fellBack {
			log.Printf("  ⚠ User %s has an invalid level_actual_amount %q, using level default: %s", user.Email, user.LevelActualAmount, feeAmount)
		}

		// Vytvoříme fee záznam
		fee, err := queries.CreateFee(ctx, db.CreateFeeParams{
			UserID:      user.ID,
			LevelID:     user.LevelID,
			PeriodStart: periodStart,
			Amount:      feeAmount,
		})

		if err != nil {
			log.Printf("  ✗ Failed to create fee for %s: %v", user.Email, err)
			errors++
			continue
		}

		log.Printf("  ✓ Created fee for %s: %s Kč (fee_id: %d)", user.Email, fee.Amount, fee.ID)
		created++
	}

	log.Printf("\nSummary:")
	log.Printf("  Period: %s", periodStart.Format("2006-01"))
	log.Printf("  Total users: %d", len(users))
	log.Printf("  Created: %d", created)
	log.Printf("  Skipped (already exists): %d", skipped)
	log.Printf("  Errors: %d", errors)

	// Log cron job completion
	level := "success"
	if errors > 0 {
		level = "warning"
	}
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "cron",
		Level:     level,
		UserID:    sql.NullInt64{},
		Message:   fmt.Sprintf("Monthly fees created for %s: %d fees", periodStart.Format("2006-01"), created),
		Metadata:  sql.NullString{String: fmt.Sprintf(`{"period":"%s","created":%d,"skipped":%d,"errors":%d}`, periodStart.Format("2006-01"), created, skipped, errors), Valid: true},
	})

	if err := recordMonth(ctx, queries, settingFeesMonth, now); err != nil {
		return errors, err
	}
	return errors, nil
}
