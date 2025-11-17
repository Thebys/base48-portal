package main

import (
	"context"
	"log"
	"os"

	"github.com/joho/godotenv"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/keycloak"
)

// Jednoduchý test service account autentizace
//
// Použití:
//   go run cmd/test/test_service_account.go
//
// Co testuje:
//   1. Načtení konfigurace
//   2. Autentizace service accountu
//   3. Získání access tokenu
//   4. Volání Keycloak Admin API (seznam realm rolí)

func main() {
	// Load .env
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config load failed: %v", err)
	}
	log.Println("✓ Config loaded")

	// Check service account credentials
	if cfg.KeycloakServiceAccountClientID == "" {
		log.Fatal("❌ KEYCLOAK_SERVICE_ACCOUNT_CLIENT_ID not set in .env")
	}
	if cfg.KeycloakServiceAccountClientSecret == "" {
		log.Fatal("❌ KEYCLOAK_SERVICE_ACCOUNT_CLIENT_SECRET not set in .env")
	}
	log.Printf("✓ Service account credentials found: %s", cfg.KeycloakServiceAccountClientID)

	ctx := context.Background()

	// Test service account authentication
	log.Println("\n--- Testing Service Account Authentication ---")
	serviceClient, err := auth.NewServiceAccountClient(
		ctx,
		cfg,
		cfg.KeycloakServiceAccountClientID,
		cfg.KeycloakServiceAccountClientSecret,
	)
	if err != nil {
		log.Fatalf("❌ Service account authentication failed: %v", err)
	}
	log.Println("✓ Service account authenticated successfully")

	// Get access token
	token, err := serviceClient.GetAccessToken(ctx)
	if err != nil {
		log.Fatalf("❌ Failed to get access token: %v", err)
	}
	log.Printf("✓ Access token obtained (length: %d chars)", len(token))
	log.Printf("   First 30 chars: %s...", token[:30])

	// Check token validity
	isValid := serviceClient.IsTokenValid(ctx)
	log.Printf("✓ Token is valid: %v", isValid)

	// Test Keycloak Admin API
	log.Println("\n--- Testing Keycloak Admin API ---")
	kcClient := keycloak.NewClient(cfg, token)

	// Try to get realm roles (requires view-realm permission)
	roles, err := kcClient.GetRealmRoles(ctx)
	if err != nil {
		log.Printf("⚠ Failed to get realm roles: %v", err)
		log.Println("  This might mean the service account doesn't have 'view-realm' permission")
		log.Println("  But authentication itself worked!")
	} else {
		log.Printf("✓ Successfully fetched %d realm roles:", len(roles))
		for i, role := range roles {
			if i >= 5 {
				log.Printf("   ... and %d more", len(roles)-5)
				break
			}
			log.Printf("   - %s", role.Name)
		}
	}

	// Optional: Test with specific user ID (if provided)
	testUserID := os.Getenv("TEST_USER_ID")
	if testUserID != "" {
		log.Println("\n--- Testing User Role Operations ---")
		log.Printf("Testing with user ID: %s", testUserID)

		// Get user's current roles
		userRoles, err := kcClient.GetUserRoles(ctx, testUserID)
		if err != nil {
			log.Printf("⚠ Failed to get user roles: %v", err)
		} else {
			log.Printf("✓ User has %d roles:", len(userRoles))
			for _, role := range userRoles {
				log.Printf("   - %s", role.Name)
			}
		}

		// Check for specific role
		hasInDebt, err := kcClient.UserHasRole(ctx, testUserID, "in_debt")
		if err != nil {
			log.Printf("⚠ Failed to check in_debt role: %v", err)
		} else {
			log.Printf("✓ User has 'in_debt' role: %v", hasInDebt)
		}
	} else {
		log.Println("\n💡 Tip: Set TEST_USER_ID environment variable to test user role operations")
		log.Println("   Example: TEST_USER_ID=abc-123-def go run cmd/test/test_service_account.go")
	}

	log.Println("\n✅ All tests completed successfully!")
	log.Println("\nNext steps:")
	log.Println("  1. Make sure service account has 'manage-users' role in Keycloak")
	log.Println("     (Keycloak -> Clients -> go-member-portal-service -> Service Account Roles)")
	log.Println("  2. Try the admin API: POST /api/admin/roles/assign")
	log.Println("  3. Run cron job: go run cmd/cron/update_debt_status.go")
}
