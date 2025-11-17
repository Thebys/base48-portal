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

// Test přiřazení/odebrání rolí pomocí service accountu (BEZ přihlášeného uživatele)
//
// Použití:
//   TEST_USER_ID=23af7ae8-1559-4836-88bc-c5a5c508baf7 go run cmd/test/test_role_assign.go
//
// Co testuje:
//   1. Přihlášení service accountu (aplikace, ne uživatel)
//   2. Zobrazení současných rolí uživatele
//   3. Přiřazení role "in_debt"
//   4. Kontrola, že role byla přiřazena
//   5. Odebrání role "in_debt"
//   6. Kontrola, že role byla odebrána

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}

	// Get test user ID from environment
	testUserID := os.Getenv("TEST_USER_ID")
	if testUserID == "" {
		log.Fatal(`❌ TEST_USER_ID not set!

Usage:
  TEST_USER_ID=<keycloak-user-id> go run cmd/test/test_role_assign.go

To find user IDs, run:
  go run cmd/test/list_users.go
`)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config load failed: %v", err)
	}

	ctx := context.Background()

	// Authenticate as SERVICE ACCOUNT (not a human user!)
	log.Println("🤖 Authenticating as SERVICE ACCOUNT (not human user)...")
	serviceClient, err := auth.NewServiceAccountClient(
		ctx,
		cfg,
		cfg.KeycloakServiceAccountClientID,
		cfg.KeycloakServiceAccountClientSecret,
	)
	if err != nil {
		log.Fatalf("❌ Service account auth failed: %v", err)
	}
	log.Println("✓ Service account authenticated")

	// Get access token
	token, err := serviceClient.GetAccessToken(ctx)
	if err != nil {
		log.Fatalf("❌ Failed to get token: %v", err)
	}
	log.Println("✓ Access token obtained")

	// Create Keycloak client
	kcClient := keycloak.NewClient(cfg, token)

	log.Printf("\n--- Testing with User ID: %s ---\n", testUserID)

	// 1. Show current roles
	log.Println("1. Getting current roles...")
	currentRoles, err := kcClient.GetUserRoles(ctx, testUserID)
	if err != nil {
		log.Fatalf("❌ Failed to get user roles: %v", err)
	}
	log.Printf("✓ User has %d roles:", len(currentRoles))
	for _, role := range currentRoles {
		log.Printf("   - %s", role.Name)
	}

	// Check if user already has in_debt role
	hasInDebt, err := kcClient.UserHasRole(ctx, testUserID, "in_debt")
	if err != nil {
		log.Fatalf("❌ Failed to check in_debt role: %v", err)
	}
	log.Printf("✓ User has 'in_debt' role: %v\n", hasInDebt)

	// 2. Assign in_debt role (if not already present)
	if !hasInDebt {
		log.Println("\n2. Assigning 'in_debt' role...")
		if err := kcClient.AssignRoleToUser(ctx, testUserID, "in_debt"); err != nil {
			log.Fatalf("❌ Failed to assign role: %v", err)
		}
		log.Println("✓ Role 'in_debt' assigned successfully")

		// Verify it was assigned
		hasInDebt, err = kcClient.UserHasRole(ctx, testUserID, "in_debt")
		if err != nil {
			log.Fatalf("❌ Failed to verify role: %v", err)
		}
		if !hasInDebt {
			log.Fatal("❌ Role was NOT assigned (verification failed)")
		}
		log.Println("✓ Verified: User now has 'in_debt' role")
	} else {
		log.Println("\n2. User already has 'in_debt' role, skipping assignment")
	}

	// 3. Remove in_debt role
	log.Println("\n3. Removing 'in_debt' role...")
	if err := kcClient.RemoveRoleFromUser(ctx, testUserID, "in_debt"); err != nil {
		log.Fatalf("❌ Failed to remove role: %v", err)
	}
	log.Println("✓ Role 'in_debt' removed successfully")

	// Verify it was removed
	hasInDebt, err = kcClient.UserHasRole(ctx, testUserID, "in_debt")
	if err != nil {
		log.Fatalf("❌ Failed to verify role removal: %v", err)
	}
	if hasInDebt {
		log.Fatal("❌ Role was NOT removed (verification failed)")
	}
	log.Println("✓ Verified: User no longer has 'in_debt' role")

	// 4. Show final roles
	log.Println("\n4. Final roles:")
	finalRoles, err := kcClient.GetUserRoles(ctx, testUserID)
	if err != nil {
		log.Fatalf("❌ Failed to get final roles: %v", err)
	}
	log.Printf("✓ User now has %d roles:", len(finalRoles))
	for _, role := range finalRoles {
		log.Printf("   - %s", role.Name)
	}

	log.Println("\n✅ ALL TESTS PASSED!")
	log.Println("\n📊 Summary:")
	log.Println("   ✓ Service account authenticated (no human user needed)")
	log.Println("   ✓ Read user roles from Keycloak")
	log.Println("   ✓ Assigned 'in_debt' role")
	log.Println("   ✓ Removed 'in_debt' role")
	log.Println("\n🎉 This proves the service account can manage roles automatically!")
	log.Println("   In Keycloak audit log, you'll see: 'Service account go-member-portal-service'")
}
