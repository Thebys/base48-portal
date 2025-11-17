package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/joho/godotenv"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
)

// List all users from Keycloak using service account
//
// Použití:
//   go run cmd/test/list_users.go

type KeycloakUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Enabled  bool   `json:"enabled"`
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx := context.Background()

	// Authenticate service account
	serviceClient, err := auth.NewServiceAccountClient(
		ctx,
		cfg,
		cfg.KeycloakServiceAccountClientID,
		cfg.KeycloakServiceAccountClientSecret,
	)
	if err != nil {
		log.Fatalf("Service account auth failed: %v", err)
	}

	token, err := serviceClient.GetAccessToken(ctx)
	if err != nil {
		log.Fatalf("Failed to get token: %v", err)
	}

	// Call Keycloak Admin API to list users
	url := fmt.Sprintf("%s/admin/realms/%s/users", cfg.KeycloakURL, cfg.KeycloakRealm)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("Failed to call Keycloak API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Keycloak API error: %s - %s", resp.Status, string(body))
	}

	var users []KeycloakUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		log.Fatalf("Failed to parse response: %v", err)
	}

	fmt.Printf("\n✅ Found %d users in Keycloak:\n\n", len(users))
	fmt.Println("USER ID                              | USERNAME           | EMAIL")
	fmt.Println("-------------------------------------|--------------------|--------------------------")

	for _, user := range users {
		enabledStr := "✓"
		if !user.Enabled {
			enabledStr = "✗"
		}
		fmt.Printf("%-36s | %-18s | %s %s\n", user.ID, user.Username, user.Email, enabledStr)
	}

	fmt.Println("\n💡 Zkopíruj USER ID pro test:")
	fmt.Println("   TEST_USER_ID=<user-id> go run cmd/test/test_role_assign.go")
}
