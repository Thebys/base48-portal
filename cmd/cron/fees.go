package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
)

// runFees creates monthly membership fee records for all accepted members.
// It sends no email: debt reminders are runDebtEmails' job, which the daemon
// runs right after this.
//
// Usage:
//
//	portal-cron fees
//
// Crontab (first day of month):
//
//	0 0 1 * * cd /path/to/portal && ./portal-cron fees >> logs/fees.log 2>&1
func runFees(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	// Získáme první den aktuálního měsíce
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	log.Printf("Creating fees for period: %s", periodStart.Format("2006-01"))

	// Načteme všechny accepted členy s jejich úrovněmi
	users, err := queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		log.Fatalf("Failed to list users: %v", err)
	}

	log.Printf("Processing %d accepted members...", len(users))

	created := 0
	skipped := 0
	errors := 0

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

		// Určíme částku - vždy používáme level_actual_amount, fallback na level.amount
		feeAmount := user.LevelActualAmount
		if feeAmount == "0" || feeAmount == "" {
			feeAmount = user.LevelAmount
			log.Printf("  ⚠ User %s has no level_actual_amount, using level default: %s", user.Email, feeAmount)
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

	if errors > 0 {
		log.Println("Job completed with errors")
		return 1
	}

	log.Println("✓ Job completed successfully")
	return 0
}
