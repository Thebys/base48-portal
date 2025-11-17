package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/keycloak"
)

// KeycloakUserInfo contains info from Keycloak API
type KeycloakUserInfo struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Enabled  bool   `json:"enabled"`
}

// AdminUserListItem combines database and Keycloak info
type AdminUserListItem struct {
	DBUser           db.User
	KeycloakEnabled  *bool  // nil if not found in Keycloak
	KeycloakUsername string
	Roles            []string
	Balance          int64
}

// AdminUsersHandler shows admin overview of all users with Keycloak status and roles
// GET /admin/users
func (h *Handler) AdminUsersHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	if !user.IsAdmin() {
		http.Error(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

	ctx := r.Context()

	// Get all users from database
	dbUsers, err := h.queries.ListUsers(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		return
	}

	// Get service account token for Keycloak API
	accessToken, err := h.getServiceAccountToken(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Service account error: %v", err), http.StatusInternalServerError)
		return
	}

	kcClient := keycloak.NewClient(h.config, accessToken)

	// Fetch all Keycloak users once (more efficient than per-user requests)
	keycloakUsers, err := h.fetchAllKeycloakUsers(ctx, accessToken)
	if err != nil {
		// Log error but continue - we can still show DB data
		fmt.Printf("[AdminUsers] Warning: Failed to fetch Keycloak users: %v\n", err)
		keycloakUsers = make(map[string]KeycloakUserInfo)
	}

	// Build combined user list
	userList := make([]AdminUserListItem, 0, len(dbUsers))

	for _, dbUser := range dbUsers {
		item := AdminUserListItem{
			DBUser: dbUser,
		}

		// Get balance
		if balance, err := h.queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: dbUser.ID, Valid: true},
			UserID_2: dbUser.ID,
		}); err == nil {
			item.Balance = balance
		}

		// Match with Keycloak user
		if dbUser.KeycloakID.Valid && dbUser.KeycloakID.String != "" {
			if kcUser, found := keycloakUsers[dbUser.KeycloakID.String]; found {
				item.KeycloakEnabled = &kcUser.Enabled
				item.KeycloakUsername = kcUser.Username

				// Get user's roles from Keycloak
				if roles, err := kcClient.GetUserRoles(ctx, dbUser.KeycloakID.String); err == nil {
					roleNames := make([]string, 0, len(roles))
					for _, role := range roles {
						// Filter out default/system roles
						if !strings.HasPrefix(role.Name, "default-") &&
							!strings.HasPrefix(role.Name, "uma_") &&
							role.Name != "offline_access" {
							roleNames = append(roleNames, role.Name)
						}
					}
					item.Roles = roleNames
				}
			}
		}

		userList = append(userList, item)
	}

	// Render template
	data := map[string]interface{}{
		"Title":    "Admin - Users",
		"User":     user,
		"UserList": userList,
	}

	h.render(w, "admin_users.html", data)
}

// AdminUsersAPIHandler returns JSON list of users with Keycloak info
// GET /api/admin/users
func (h *Handler) AdminUsersAPIHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil || !user.IsAdmin() {
		h.jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	// Get all users from database
	dbUsers, err := h.queries.ListUsers(ctx)
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		return
	}

	// Get service account token for Keycloak API
	accessToken, err := h.getServiceAccountToken(ctx)
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Service account error: %v", err), http.StatusInternalServerError)
		return
	}

	kcClient := keycloak.NewClient(h.config, accessToken)

	// Fetch all Keycloak users
	keycloakUsers, err := h.fetchAllKeycloakUsers(ctx, accessToken)
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Keycloak error: %v", err), http.StatusInternalServerError)
		return
	}

	// Build response
	type UserResponse struct {
		ID               int64    `json:"id"`
		Email            string   `json:"email"`
		Realname         string   `json:"realname"`
		State            string   `json:"state"`
		Balance          int64    `json:"balance"`
		KeycloakID       string   `json:"keycloak_id"`
		KeycloakEnabled  *bool    `json:"keycloak_enabled"`
		KeycloakUsername string   `json:"keycloak_username"`
		Roles            []string `json:"roles"`
	}

	response := make([]UserResponse, 0, len(dbUsers))

	for _, dbUser := range dbUsers {
		userResp := UserResponse{
			ID:       dbUser.ID,
			Email:    dbUser.Email,
			Realname: dbUser.Realname.String,
			State:    dbUser.State,
		}

		// Get balance
		if balance, err := h.queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: dbUser.ID, Valid: true},
			UserID_2: dbUser.ID,
		}); err == nil {
			userResp.Balance = balance
		}

		// Keycloak info
		if dbUser.KeycloakID.Valid && dbUser.KeycloakID.String != "" {
			userResp.KeycloakID = dbUser.KeycloakID.String

			if kcUser, found := keycloakUsers[dbUser.KeycloakID.String]; found {
				userResp.KeycloakEnabled = &kcUser.Enabled
				userResp.KeycloakUsername = kcUser.Username

				// Get roles
				if roles, err := kcClient.GetUserRoles(ctx, dbUser.KeycloakID.String); err == nil {
					roleNames := make([]string, 0)
					for _, role := range roles {
						if !strings.HasPrefix(role.Name, "default-") &&
							!strings.HasPrefix(role.Name, "uma_") &&
							role.Name != "offline_access" {
							roleNames = append(roleNames, role.Name)
						}
					}
					userResp.Roles = roleNames
				}
			}
		}

		response = append(response, userResp)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"users":   response,
	})
}


// fetchAllKeycloakUsers fetches all users from Keycloak API and returns them as a map
func (h *Handler) fetchAllKeycloakUsers(ctx context.Context, accessToken string) (map[string]KeycloakUserInfo, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users", h.config.KeycloakURL, h.config.KeycloakRealm)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("keycloak API error: %s - %s", resp.Status, string(body))
	}

	var users []KeycloakUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	// Convert to map for fast lookups
	userMap := make(map[string]KeycloakUserInfo, len(users))
	for _, user := range users {
		userMap[user.ID] = user
	}

	return userMap, nil
}
