package main

import (
	"context"
	"database/sql"
	"log"

	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/keycloak"
)

// Příklad cron jobu: Automatická aktualizace role in_debt na základě balance
//
// Použití:
//   go run cmd/cron/update_debt_status.go
//
// Nebo v crontab:
//   0 2 * * * cd /path/to/portal && ./update_debt_status >> logs/cron.log 2>&1

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Check service account credentials
	if cfg.KeycloakServiceAccountClientID == "" || cfg.KeycloakServiceAccountClientSecret == "" {
		log.Fatal("KEYCLOAK_SERVICE_ACCOUNT_CLIENT_ID and KEYCLOAK_SERVICE_ACCOUNT_CLIENT_SECRET are required")
	}

	// Connect to database
	database, err := sql.Open("sqlite", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	queries := db.New(database)

	ctx := context.Background()

	// Create service account client (uses application credentials, not user)
	serviceClient, err := auth.NewServiceAccountClient(
		ctx,
		cfg,
		cfg.KeycloakServiceAccountClientID,
		cfg.KeycloakServiceAccountClientSecret,
	)
	if err != nil {
		log.Fatalf("Failed to create service account: %v", err)
	}

	log.Println("✓ Service account authenticated")

	// Get access token for Keycloak API
	token, err := serviceClient.GetAccessToken(ctx)
	if err != nil {
		log.Fatalf("Failed to get access token: %v", err)
	}

	// Create Keycloak client
	kcClient := keycloak.NewClient(cfg, token)

	// Get all users from database
	users, err := queries.ListUsers(ctx)
	if err != nil {
		log.Fatalf("Failed to list users: %v", err)
	}

	log.Printf("Processing %d users...", len(users))

	updated := 0
	errors := 0

	for _, user := range users {
		// Skip users without Keycloak ID
		if !user.KeycloakID.Valid || user.KeycloakID.String == "" {
			continue
		}

		keycloakID := user.KeycloakID.String

		// Get user's balance
		balance, err := queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
			UserID_2: user.ID,
		})
		if err != nil {
			log.Printf("⚠ Error getting balance for user %s: %v", user.Email, err)
			errors++
			continue
		}

		// Check if user has in_debt role
		hasDebtRole, err := kcClient.UserHasRole(ctx, keycloakID, "in_debt")
		if err != nil {
			log.Printf("⚠ Error checking roles for user %s: %v", user.Email, err)
			errors++
			continue
		}

		// Determine if user should have in_debt role (balance < 0)
		shouldHaveDebt := balance < 0

		// Update role if needed
		if shouldHaveDebt && !hasDebtRole {
			// User is in debt but doesn't have the role - assign it
			if err := kcClient.AssignRoleToUser(ctx, keycloakID, "in_debt"); err != nil {
				log.Printf("✗ Failed to assign in_debt to %s: %v", user.Email, err)
				errors++
			} else {
				log.Printf("✓ Assigned in_debt to %s (balance: %d)", user.Email, balance)
				updated++
			}
		} else if !shouldHaveDebt && hasDebtRole {
			// User paid off debt but still has the role - remove it
			if err := kcClient.RemoveRoleFromUser(ctx, keycloakID, "in_debt"); err != nil {
				log.Printf("✗ Failed to remove in_debt from %s: %v", user.Email, err)
				errors++
			} else {
				log.Printf("✓ Removed in_debt from %s (balance: %d)", user.Email, balance)
				updated++
			}
		}
	}

	log.Printf("\nSummary:")
	log.Printf("  Total users: %d", len(users))
	log.Printf("  Updated: %d", updated)
	log.Printf("  Errors: %d", errors)

	if errors > 0 {
		log.Fatal("Job completed with errors")
	}

	log.Println("✓ Job completed successfully")
}
