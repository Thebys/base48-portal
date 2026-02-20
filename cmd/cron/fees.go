package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
	"github.com/base48/member-portal/internal/qrpay"
)

// runFees creates monthly membership fee records for all accepted members
// and sends debt warning emails where appropriate.
//
// Usage:
//
//	portal-cron fees
//
// Crontab (first day of month):
//
//	0 0 1 * * cd /path/to/portal && ./portal-cron fees >> logs/fees.log 2>&1
func runFees(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	qrService := qrpay.NewService(cfg.BankIBAN, cfg.BankBIC)
	emailClient := email.New(cfg, queries, qrService)
	emailClient.DefaultDelay = 72 * time.Hour // Delay debt emails so admin can review/cancel

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
	emailsSent := 0

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

		// Po vytvoření fee zkontrolujeme balance a případně pošleme upozornění
		balance, err := queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
			UserID_2: user.ID,
		})
		if err != nil {
			log.Printf("  ⚠ Failed to get balance for %s: %v", user.Email, err)
			continue
		}

		// Zkontrolujeme dluh a pošleme příslušný e-mail
		var monthlyFee float64
		fmt.Sscanf(feeAmount, "%f", &monthlyFee)
		balanceFloat := float64(balance)

		if monthlyFee > 0 && (balanceFloat <= -(2*monthlyFee) || balanceFloat <= -monthlyFee) {
			fullUser, err := queries.GetUserByID(ctx, user.ID)
			if err != nil {
				log.Printf("  ⚠ Failed to get user record for email: %v", err)
				continue
			}

			if balanceFloat <= -(2 * monthlyFee) {
				// Tier 2: dluh >= 2× fee — závažné upozornění
				if err := emailClient.SendDebtWarning(ctx, &fullUser, balanceFloat, monthlyFee); err != nil {
					log.Printf("  ⚠ Failed to send debt warning email: %v", err)
				} else {
					log.Printf("  ✉ Sent debt warning email (balance: %.0f Kč)", balanceFloat)
					emailsSent++
				}
			} else {
				// Tier 1: dluh >= 1× fee — mírné upozornění
				if err := emailClient.SendNegativeBalance(ctx, &fullUser, balanceFloat, monthlyFee); err != nil {
					log.Printf("  ⚠ Failed to send negative balance email: %v", err)
				} else {
					log.Printf("  ✉ Sent negative balance email (balance: %.0f Kč)", balanceFloat)
					emailsSent++
				}
			}
		}
	}

	log.Printf("\nSummary:")
	log.Printf("  Period: %s", periodStart.Format("2006-01"))
	log.Printf("  Total users: %d", len(users))
	log.Printf("  Created: %d", created)
	log.Printf("  Skipped (already exists): %d", skipped)
	log.Printf("  Debt warning emails sent: %d", emailsSent)
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
		Message:   fmt.Sprintf("Monthly fees created for %s: %d fees, %d emails sent", periodStart.Format("2006-01"), created, emailsSent),
		Metadata:  sql.NullString{String: fmt.Sprintf(`{"period":"%s","created":%d,"skipped":%d,"emails":%d,"errors":%d}`, periodStart.Format("2006-01"), created, skipped, emailsSent, errors), Valid: true},
	})

	if errors > 0 {
		log.Println("Job completed with errors")
		return 1
	}

	log.Println("✓ Job completed successfully")
	return 0
}
