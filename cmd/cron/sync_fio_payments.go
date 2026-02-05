package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/fio"
)

// Sync payments from FIO Bank API to local database
//
// Usage:
//   go run cmd/cron/sync_fio_payments.go
//   go run cmd/cron/sync_fio_payments.go --since-last  # Fetch only new transactions
//   go run cmd/cron/sync_fio_payments.go --days 7      # Fetch last 7 days
//
// Nebo v crontab (každý den ve 3:00):
//   0 3 * * * cd /path/to/portal && ./sync_fio_payments --since-last >> logs/fio-sync.log 2>&1

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Check FIO token
	if cfg.BankFIOToken == "" {
		log.Fatal("BANK_FIO_TOKEN is required")
	}

	// Connect to database
	database, err := sql.Open("sqlite", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	queries := db.New(database)
	ctx := context.Background()

	// Create FIO API client
	fioClient := fio.NewClient(cfg.BankFIOToken)

	// Determine which transactions to fetch
	var transactions []fio.Transaction
	var fetchErr error

	// Default: fetch last 85 days (FIO API limit is 90, using 85 for safety margin)
	// You can modify this based on command line arguments
	daysBack := 85
	dateFrom := time.Now().AddDate(0, 0, -daysBack)
	dateTo := time.Now()

	log.Printf("Fetching FIO transactions from %s to %s...",
		fio.FormatDate(dateFrom), fio.FormatDate(dateTo))

	transactions, fetchErr = fioClient.FetchTransactionsByPeriod(
		ctx,
		fio.FormatDate(dateFrom),
		fio.FormatDate(dateTo),
	)

	if fetchErr != nil {
		log.Fatalf("Failed to fetch transactions: %v", fetchErr)
	}

	log.Printf("Fetched %d transactions from FIO API", len(transactions))

	if len(transactions) == 0 {
		log.Println("✓ No new transactions to sync")
		return
	}

	// Process transactions
	inserted := 0
	updated := 0
	skipped := 0
	errors := 0
	unmatchedVS := []fio.Transaction{}
	emptyVS := []fio.Transaction{}

	for _, tx := range transactions {
		// Skip transactions with zero or negative amounts (outgoing payments, fees, etc.)
		// Only process incoming payments (positive amounts)
		if tx.Amount <= 0 {
			skipped++
			continue
		}

		// Try to match user by variable symbol (payments_id)
		// IMPORTANT: VS is NOT the user.id, it's the user.payments_id!
		// Edge case: Some users put VS in Message field instead of VS field
		variableSymbol := tx.VariableSymbol
		if variableSymbol == "" && tx.Message != "" {
			// Check if Message contains only digits (likely a VS)
			isNumeric := true
			for _, ch := range tx.Message {
				if ch < '0' || ch > '9' {
					isNumeric = false
					break
				}
			}
			if isNumeric {
				variableSymbol = tx.Message
				log.Printf("ℹ Using Message field as VS: '%s' (%.2f CZK from %s)", variableSymbol, tx.Amount, tx.AccountName)
			}
		}

		var userID sql.NullInt64
		if variableSymbol != "" {
			// Look up user by payments_id (VS), not by user.id
			if user, err := queries.GetUserByPaymentsID(ctx, sql.NullString{String: variableSymbol, Valid: true}); err == nil {
				userID = sql.NullInt64{Int64: user.ID, Valid: true}
			} else if err == sql.ErrNoRows {
				log.Printf("⚠ User with payments_id (VS) '%s' not found in database (%.2f CZK from %s)",
					variableSymbol, tx.Amount, tx.AccountName)
				unmatchedVS = append(unmatchedVS, tx)
			} else {
				log.Printf("⚠ Database error looking up user by payments_id '%s': %v", variableSymbol, err)
				errors++
			}
		} else {
			if tx.Amount > 0 {
				log.Printf("⚠ Empty VS - %.2f CZK from %s", tx.Amount, tx.AccountName)
				emptyVS = append(emptyVS, tx)
			}
		}

		// Parse transaction date
		txDate, err := fio.ParseDate(tx.Date)
		if err != nil {
			log.Printf("⚠ Failed to parse date %s: %v", tx.Date, err)
			txDate = time.Now() // fallback
		}

		// Prepare raw data JSON
		rawDataJSON, err := json.Marshal(tx)
		if err != nil {
			log.Printf("⚠ Failed to marshal transaction data: %v", err)
			rawDataJSON = []byte("{}")
		}

		// Build remote account string (account + bank code)
		remoteAccount := tx.AccountNumber
		if tx.BankCode != "" {
			remoteAccount = fmt.Sprintf("%s/%s", tx.AccountNumber, tx.BankCode)
		}

		// Check if payment already exists
		existingPayment, err := queries.GetPaymentByKindAndID(ctx, db.GetPaymentByKindAndIDParams{
			Kind:   "fio",
			KindID: fmt.Sprintf("%d", tx.ID),
		})

		if err == sql.ErrNoRows {
			// Insert new payment
			_, err = queries.UpsertPayment(ctx, db.UpsertPaymentParams{
				UserID:         userID,
				ProjectID:      sql.NullInt64{}, // Not set during FIO import
				Date:           txDate,
				Amount:         fmt.Sprintf("%.2f", tx.Amount),
				Kind:           "fio",
				KindID:         fmt.Sprintf("%d", tx.ID),
				LocalAccount:   "FIO", // Could be parsed from API info
				RemoteAccount:  remoteAccount,
				Identification: variableSymbol,
				RawData:        sql.NullString{String: string(rawDataJSON), Valid: true},
				StaffComment:   sql.NullString{},
			})

			if err != nil {
				log.Printf("✗ Failed to insert payment (FIO ID %d): %v", tx.ID, err)
				errors++
			} else {
				log.Printf("✓ Inserted payment: %.2f CZK from %s (VS: %s, FIO ID: %d)",
					tx.Amount, tx.AccountName, tx.VariableSymbol, tx.ID)
				inserted++
			}
		} else if err != nil {
			log.Printf("⚠ Error checking existing payment: %v", err)
			errors++
		} else {
			// Payment exists - check if it needs update
			needsUpdate := false

			// Check if user_id changed (manual assignment)
			if userID.Valid && (!existingPayment.UserID.Valid || existingPayment.UserID.Int64 != userID.Int64) {
				needsUpdate = true
			}

			if needsUpdate {
				_, err = queries.UpsertPayment(ctx, db.UpsertPaymentParams{
					UserID:         userID,
					ProjectID:      existingPayment.ProjectID, // Preserve project assignment
					Date:           txDate,
					Amount:         fmt.Sprintf("%.2f", tx.Amount),
					Kind:           "fio",
					KindID:         fmt.Sprintf("%d", tx.ID),
					LocalAccount:   "FIO",
					RemoteAccount:  remoteAccount,
					Identification: variableSymbol,
					RawData:        sql.NullString{String: string(rawDataJSON), Valid: true},
					StaffComment:   existingPayment.StaffComment, // Preserve staff comment
				})

				if err != nil {
					log.Printf("✗ Failed to update payment (FIO ID %d): %v", tx.ID, err)
					errors++
				} else {
					log.Printf("↻ Updated payment: %.2f CZK (FIO ID: %d)", tx.Amount, tx.ID)
					updated++
				}
			} else {
				// No changes needed
				skipped++
			}
		}
	}

	log.Println("\n" + repeat("=", 80))
	log.Println("SYNC SUMMARY")
	log.Println(repeat("=", 80))
	log.Printf("Total transactions fetched: %d", len(transactions))
	log.Printf("  ✓ Inserted: %d", inserted)
	log.Printf("  ↻ Updated: %d", updated)
	log.Printf("  - Skipped (negative/zero): %d", skipped)
	log.Printf("  ✗ Errors: %d", errors)
	log.Println(repeat("-", 80))

	// Report problematic payments
	totalUnmatched := len(unmatchedVS) + len(emptyVS)
	if totalUnmatched > 0 {
		log.Printf("\n⚠️  PROBLEMATIC PAYMENTS: %d", totalUnmatched)

		if len(emptyVS) > 0 {
			totalAmount := 0.0
			log.Printf("\n  📝 Empty variable symbol: %d payments", len(emptyVS))
			for _, tx := range emptyVS {
				totalAmount += tx.Amount
				log.Printf("     - %.2f CZK from %s on %s", tx.Amount, tx.AccountName, tx.Date[:10])
			}
			log.Printf("     Total: %.2f CZK", totalAmount)
		}

		if len(unmatchedVS) > 0 {
			totalAmount := 0.0
			log.Printf("\n  ❌ User not found: %d payments", len(unmatchedVS))
			for _, tx := range unmatchedVS {
				totalAmount += tx.Amount
				log.Printf("     - %.2f CZK (VS/payments_id: %s) from %s", tx.Amount, tx.VariableSymbol, tx.AccountName)
			}
			log.Printf("     Total: %.2f CZK", totalAmount)
			log.Println("\n     💡 These payments have VS that doesn't match any user's payments_id.")
			log.Println("        Check if users need to be imported or VS is incorrect.")
		}

		log.Printf("\n💡 Run 'go run cmd/cron/report_unmatched_payments.go' for detailed report")
	}

	log.Println("\n" + repeat("=", 80))

	// Log FIO sync completion
	level := "success"
	if errors > 0 {
		level = "warning"
	} else if totalUnmatched > 0 {
		level = "info"
	}
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "fio_sync",
		Level:     level,
		UserID:    sql.NullInt64{},
		Message:   fmt.Sprintf("FIO sync completed: %d new, %d updated, %d unmatched", inserted, updated, totalUnmatched),
		Metadata:  sql.NullString{String: fmt.Sprintf(`{"inserted":%d,"updated":%d,"skipped":%d,"unmatched":%d,"errors":%d}`, inserted, updated, skipped, totalUnmatched, errors), Valid: true},
	})

	if errors > 0 {
		log.Fatal("Job completed with errors")
	}

	log.Println("✓ Job completed successfully")
}

// Helper to repeat strings (since strings.Repeat might not be imported)
func repeat(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
