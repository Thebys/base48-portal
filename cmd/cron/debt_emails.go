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

// runDebtEmails checks all accepted members' balances and queues debt warning
// emails for those with negative balances. Safe to run repeatedly — anti-spam
// check prevents sending the same email type to the same user within 30 days.
//
// Usage:
//
//	portal-cron debt-emails
func runDebtEmails(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	qrService := qrpay.NewService(cfg.BankIBAN, cfg.BankBIC)
	emailClient := email.New(cfg, queries, qrService)
	emailClient.DefaultDelay = 72 * time.Hour // Queue into outbox; admin has 72h to review/cancel

	log.Println("Checking member balances for debt emails...")

	users, err := queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		log.Fatalf("Failed to list users: %v", err)
	}

	log.Printf("Processing %d accepted members...", len(users))

	queued := 0
	skipped := 0
	noDebt := 0
	errors := 0

	for _, user := range users {
		// Determine monthly fee
		feeAmount := user.LevelActualAmount
		if feeAmount == "0" || feeAmount == "" {
			feeAmount = user.LevelAmount
		}
		var monthlyFee float64
		fmt.Sscanf(feeAmount, "%f", &monthlyFee)
		if monthlyFee <= 0 {
			continue
		}

		// Check balance
		balance, err := queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
			UserID_2: user.ID,
		})
		if err != nil {
			log.Printf("  ⚠ Failed to get balance for %s: %v", user.Email, err)
			errors++
			continue
		}

		balanceFloat := float64(balance)

		// Determine which email to send (if any)
		var templateName string
		if balanceFloat <= -(2 * monthlyFee) {
			templateName = "debt_warning.html"
		} else if balanceFloat <= -monthlyFee {
			templateName = "negative_balance.html"
		} else {
			noDebt++
			continue
		}

		// Anti-spam: skip if this email type was already sent/queued in last 30 days
		recent, err := queries.GetRecentEmailByUserAndTemplate(ctx, db.GetRecentEmailByUserAndTemplateParams{
			UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
			TemplateName: templateName,
		})
		if err == nil && recent.ID > 0 {
			log.Printf("  ⊘ Skipping %s — %s already queued on %s", user.Email, templateName, recent.CreatedAt.Format("2006-01-02"))
			skipped++
			continue
		}

		// Get full user record for email sending
		fullUser, err := queries.GetUserByID(ctx, user.ID)
		if err != nil {
			log.Printf("  ⚠ Failed to get user record for %s: %v", user.Email, err)
			errors++
			continue
		}

		// Queue the email into outbox
		if templateName == "debt_warning.html" {
			if err := emailClient.SendDebtWarning(ctx, &fullUser, balanceFloat, monthlyFee); err != nil {
				log.Printf("  ⚠ Failed to queue debt warning for %s: %v", user.Email, err)
				errors++
			} else {
				log.Printf("  ✉ Queued debt warning for %s (balance: %.0f Kč)", user.Email, balanceFloat)
				queued++
			}
		} else {
			if err := emailClient.SendNegativeBalance(ctx, &fullUser, balanceFloat, monthlyFee); err != nil {
				log.Printf("  ⚠ Failed to queue negative balance email for %s: %v", user.Email, err)
				errors++
			} else {
				log.Printf("  ✉ Queued negative balance email for %s (balance: %.0f Kč)", user.Email, balanceFloat)
				queued++
			}
		}
	}

	log.Printf("\nSummary:")
	log.Printf("  Total members: %d", len(users))
	log.Printf("  No debt: %d", noDebt)
	log.Printf("  Emails queued: %d", queued)
	log.Printf("  Skipped (already sent): %d", skipped)
	log.Printf("  Errors: %d", errors)

	level := "success"
	if errors > 0 {
		level = "warning"
	}
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "cron",
		Level:     level,
		UserID:    sql.NullInt64{},
		Message:   fmt.Sprintf("Debt emails: %d queued, %d skipped, %d errors", queued, skipped, errors),
		Metadata:  sql.NullString{String: fmt.Sprintf(`{"queued":%d,"skipped":%d,"no_debt":%d,"errors":%d}`, queued, skipped, noDebt, errors), Valid: true},
	})

	if errors > 0 {
		log.Println("Job completed with errors")
		return 1
	}

	log.Println("✓ Job completed successfully")
	return 0
}
