package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
	"github.com/base48/member-portal/internal/fio"
	"github.com/base48/member-portal/internal/keycloak"
)

// Sync payments from FIO Bank API and update debt status in Keycloak
//
// Usage:
//   go run cmd/cron/sync_fio_payments.go
//
// Crontab (every 2 minutes):
//   */2 * * * * cd /path/to/portal && ./sync_fio_payments >> logs/fio-sync.log 2>&1

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.BankFIOToken == "" {
		log.Fatal("BANK_FIO_TOKEN is required")
	}

	database, err := sql.Open("sqlite", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	queries := db.New(database)
	ctx := context.Background()

	fioErrors := syncFIO(ctx, cfg, queries)
	debtErrors := updateDebtStatus(ctx, cfg, queries)
	processScheduledEmails(ctx, cfg, queries)

	if fioErrors+debtErrors > 0 {
		log.Fatal("Job completed with errors")
	}
	log.Println("✓ Job completed successfully")
}

func syncFIO(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	fioClient := fio.NewClient(cfg.BankFIOToken)

	// Default: fetch last 85 days (FIO API limit is 90, using 85 for safety margin)
	daysBack := 85
	dateFrom := time.Now().AddDate(0, 0, -daysBack)
	dateTo := time.Now()

	log.Printf("Fetching FIO transactions from %s to %s...",
		fio.FormatDate(dateFrom), fio.FormatDate(dateTo))

	result, err := fioClient.FetchTransactionsByPeriod(
		ctx,
		fio.FormatDate(dateFrom),
		fio.FormatDate(dateTo),
	)
	if err != nil {
		log.Printf("✗ Failed to fetch transactions: %v", err)
		return 1
	}

	transactions := result.Transactions

	// Cache FIO closing balance
	queries.UpsertSetting(ctx, db.UpsertSettingParams{
		Key:   "fio_closing_balance",
		Value: fmt.Sprintf("%.0f", result.ClosingBalance),
	})
	queries.UpsertSetting(ctx, db.UpsertSettingParams{
		Key:   "fio_closing_balance_date",
		Value: fio.FormatDate(dateTo),
	})
	log.Printf("Cached FIO closing balance: %.0f CZK", result.ClosingBalance)

	log.Printf("Fetched %d transactions from FIO API", len(transactions))

	if len(transactions) == 0 {
		log.Println("✓ No new transactions to sync")
		return 0
	}

	// Process transactions
	inserted := 0
	updated := 0
	skipped := 0
	errors := 0
	unmatchedVS := []fio.Transaction{}
	emptyVS := []fio.Transaction{}

	for _, tx := range transactions {
		// Detect rent payments (outgoing, VS 3872751 or message contains "najemne"/"nájem")
		if tx.Amount < 0 {
			msg := strings.ToLower(tx.Message + " " + tx.Comment)
			isRent := tx.VariableSymbol == "3872751" ||
				strings.Contains(msg, "najemne") || strings.Contains(msg, "nájem")
			if isRent {
				if txDate, parseErr := fio.ParseDate(tx.Date); parseErr == nil {
					shouldUpdate := true
					if existing, getErr := queries.GetSetting(ctx, "fio_last_rent_date"); getErr == nil {
						if existingDate, _ := time.Parse("2006-01-02", existing.Value); !txDate.After(existingDate) {
							shouldUpdate = false
						}
					}
					if shouldUpdate {
						queries.UpsertSetting(ctx, db.UpsertSettingParams{
							Key:   "fio_last_rent_date",
							Value: txDate.Format("2006-01-02"),
						})
						queries.UpsertSetting(ctx, db.UpsertSettingParams{
							Key:   "fio_last_rent_amount",
							Value: fmt.Sprintf("%.0f", -tx.Amount),
						})
						log.Printf("✓ Detected rent payment: %.0f CZK on %s", -tx.Amount, txDate.Format("2006-01-02"))
					}
				}
			}
		}

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
		var projectID sql.NullInt64
		if variableSymbol != "" {
			// Look up user by payments_id (VS), not by user.id
			if user, err := queries.GetUserByPaymentsID(ctx, sql.NullString{String: variableSymbol, Valid: true}); err == nil {
				userID = sql.NullInt64{Int64: user.ID, Valid: true}
			} else if err == sql.ErrNoRows {
				// Not a user payment — check if it belongs to a fundraising project
				if project, err := queries.GetProjectByPaymentsID(ctx, variableSymbol); err == nil {
					projectID = sql.NullInt64{Int64: project.ID, Valid: true}
					log.Printf("✓ Matched payment VS '%s' to project '%s' (%.2f CZK from %s)",
						variableSymbol, project.Name, tx.Amount, tx.AccountName)
				} else {
					log.Printf("⚠ User with payments_id (VS) '%s' not found in database (%.2f CZK from %s)",
						variableSymbol, tx.Amount, tx.AccountName)
					unmatchedVS = append(unmatchedVS, tx)
				}
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
				ProjectID:      projectID,
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

			// Check if project_id should be set (new project match)
			if projectID.Valid && !existingPayment.ProjectID.Valid {
				needsUpdate = true
			}

			// Use matched projectID if set, otherwise preserve existing
			effectiveProjectID := projectID
			if existingPayment.ProjectID.Valid {
				effectiveProjectID = existingPayment.ProjectID
			}

			if needsUpdate {
				_, err = queries.UpsertPayment(ctx, db.UpsertPaymentParams{
					UserID:         userID,
					ProjectID:      effectiveProjectID,
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

	return errors
}

func updateDebtStatus(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	if cfg.KeycloakServiceAccountClientID == "" || cfg.KeycloakServiceAccountClientSecret == "" {
		log.Println("ℹ Skipping debt status update (no Keycloak service account credentials)")
		return 0
	}

	log.Println("\n" + repeat("=", 80))
	log.Println("DEBT STATUS UPDATE")
	log.Println(repeat("=", 80))

	serviceClient, err := auth.NewServiceAccountClient(
		ctx, cfg,
		cfg.KeycloakServiceAccountClientID,
		cfg.KeycloakServiceAccountClientSecret,
	)
	if err != nil {
		log.Printf("✗ Failed to create service account: %v", err)
		return 1
	}

	token, err := serviceClient.GetAccessToken(ctx)
	if err != nil {
		log.Printf("✗ Failed to get access token: %v", err)
		return 1
	}

	kcClient := keycloak.NewClient(cfg, token)

	users, err := queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		log.Printf("✗ Failed to list accepted users: %v", err)
		return 1
	}

	log.Printf("Checking debt and active_member status for %d accepted members...", len(users))

	debtAssigned := 0
	debtRemoved := 0
	activeMemberAssigned := 0
	errors := 0

	for _, user := range users {
		if !user.KeycloakID.Valid || user.KeycloakID.String == "" {
			continue
		}

		keycloakID := user.KeycloakID.String

		balance, err := queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
			UserID_2: user.ID,
		})
		if err != nil {
			log.Printf("  ⚠ Error getting balance for %s: %v", user.Email, err)
			errors++
			continue
		}

		// Fetch all roles once (instead of multiple UserHasRole calls)
		roles, err := kcClient.GetUserRoles(ctx, keycloakID)
		if err != nil {
			log.Printf("  ⚠ Error checking roles for %s: %v", user.Email, err)
			errors++
			continue
		}

		hasDebtRole := false
		hasActiveMemberRole := false
		for _, role := range roles {
			switch role.Name {
			case "in_debt":
				hasDebtRole = true
			case "active_member":
				hasActiveMemberRole = true
			}
		}

		// Sync in_debt role based on balance
		shouldHaveDebt := balance < 0

		if shouldHaveDebt && !hasDebtRole {
			if err := kcClient.AssignRoleToUser(ctx, keycloakID, "in_debt"); err != nil {
				log.Printf("  ✗ Failed to assign in_debt to %s: %v", user.Email, err)
				errors++
			} else {
				log.Printf("  ✓ Assigned in_debt to %s (balance: %d)", user.Email, balance)
				debtAssigned++
			}
		} else if !shouldHaveDebt && hasDebtRole {
			if err := kcClient.RemoveRoleFromUser(ctx, keycloakID, "in_debt"); err != nil {
				log.Printf("  ✗ Failed to remove in_debt from %s: %v", user.Email, err)
				errors++
			} else {
				log.Printf("  ✓ Removed in_debt from %s (balance: %d)", user.Email, balance)
				debtRemoved++
			}
		}

		// Sync active_member role (all accepted members should have it)
		if !hasActiveMemberRole {
			if err := kcClient.AssignRoleToUser(ctx, keycloakID, "active_member"); err != nil {
				log.Printf("  ✗ Failed to assign active_member to %s: %v", user.Email, err)
				errors++
			} else {
				log.Printf("  ✓ Assigned active_member to %s", user.Email)
				activeMemberAssigned++
			}
		}
	}

	log.Printf("\nRole sync summary:")
	log.Printf("  Total accepted users: %d", len(users))
	log.Printf("  in_debt assigned: %d", debtAssigned)
	log.Printf("  in_debt removed: %d", debtRemoved)
	log.Printf("  active_member assigned: %d", activeMemberAssigned)
	log.Printf("  Errors: %d", errors)

	level := "success"
	if errors > 0 {
		level = "warning"
	}
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "keycloak",
		Level:     level,
		UserID:    sql.NullInt64{},
		Message:   fmt.Sprintf("Role sync: in_debt +%d/-%d, active_member +%d", debtAssigned, debtRemoved, activeMemberAssigned),
		Metadata:  sql.NullString{String: fmt.Sprintf(`{"debt_assigned":%d,"debt_removed":%d,"active_member_assigned":%d,"errors":%d}`, debtAssigned, debtRemoved, activeMemberAssigned, errors), Valid: true},
	})

	return errors
}

func processScheduledEmails(ctx context.Context, cfg *config.Config, queries *db.Queries) {
	emailClient := email.New(cfg, queries, nil)
	emailClient.ProcessPendingEmails(ctx)
}

func repeat(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
