package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/keycloak"
)

// KeycloakUserInfo contains info from Keycloak API
type KeycloakUserInfo struct {
	ID         string              `json:"id"`
	Username   string              `json:"username"`
	Email      string              `json:"email"`
	FirstName  string              `json:"firstName"`
	LastName   string              `json:"lastName"`
	Enabled    bool                `json:"enabled"`
	Attributes map[string][]string `json:"attributes"`
}

// AdminUserListItem combines database and Keycloak info
type AdminUserListItem struct {
	DBUser           db.User
	KeycloakLinked   bool // true if found in Keycloak
	KeycloakUsername string
	Roles            []string
	Balance          int64
}

// AdminUsersHandler shows admin overview of all users with Keycloak status and roles
// GET /admin/users
func (h *Handler) AdminUsersHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	ctx := r.Context()

	// Get filter and sort parameters from query string
	// Default: show accepted members sorted by balance ascending (debt first)
	filterState := r.URL.Query().Get("state")
	filterKeycloak := r.URL.Query().Get("keycloak")
	filterBalance := r.URL.Query().Get("balance")
	filterSearch := strings.ToLower(r.URL.Query().Get("search"))
	sortBy := r.URL.Query().Get("sort")
	if len(r.URL.Query()) == 0 {
		filterState = "accepted"
		sortBy = "balance_asc"
	}

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

	// Batch-fetch all user balances in one query (avoids N+1)
	balanceMap, err := h.getUserBalanceMap(ctx)
	if err != nil {
		fmt.Printf("[AdminUsers] Warning: Failed to batch-fetch balances: %v\n", err)
		balanceMap = make(map[int64]int64)
	}

	// Build combined user list with filtering
	userList := make([]AdminUserListItem, 0, len(dbUsers))

	for _, dbUser := range dbUsers {
		item := AdminUserListItem{
			DBUser:  dbUser,
			Balance: balanceMap[dbUser.ID],
		}

		// Match with Keycloak user
		if dbUser.KeycloakID.Valid && dbUser.KeycloakID.String != "" {
			if kcUser, found := keycloakUsers[dbUser.KeycloakID.String]; found {
				item.KeycloakLinked = true
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

		// Apply filters
		if !matchesFilters(item, filterState, filterKeycloak, filterBalance, filterSearch) {
			continue
		}

		userList = append(userList, item)
	}

	// Apply sorting
	sortUserList(userList, sortBy)

	// Render template
	data := map[string]interface{}{
		"Title":          "Admin - Users",
		"User":           user,
		"UserList":       userList,
		"FilterState":    filterState,
		"FilterKeycloak": filterKeycloak,
		"FilterBalance":  filterBalance,
		"FilterSearch":   r.URL.Query().Get("search"), // Original case
		"SortBy":         sortBy,
	}

	h.render(w, "admin_users.html", data)
}

// matchesFilters checks if a user item matches the given filter criteria
func matchesFilters(item AdminUserListItem, state, keycloak, balance, search string) bool {
	// Filter by state
	if state != "" && item.DBUser.State != state {
		return false
	}

	// Filter by Keycloak status
	if keycloak != "" {
		switch keycloak {
		case "linked":
			if !item.DBUser.KeycloakID.Valid || item.DBUser.KeycloakID.String == "" {
				return false
			}
		case "not_linked":
			if item.DBUser.KeycloakID.Valid && item.DBUser.KeycloakID.String != "" {
				return false
			}
		case "enabled", "disabled":
			// Keycloak enabled/disabled filtering removed — use linked/not_linked instead
		}
	}

	// Filter by balance
	if balance != "" {
		switch balance {
		case "positive":
			if item.Balance < 0 {
				return false
			}
		case "negative":
			if item.Balance >= 0 {
				return false
			}
		}
	}

	// Filter by search (email or realname)
	if search != "" {
		emailMatch := strings.Contains(strings.ToLower(item.DBUser.Email), search)
		nameMatch := item.DBUser.Realname.Valid && strings.Contains(strings.ToLower(item.DBUser.Realname.String), search)
		if !emailMatch && !nameMatch {
			return false
		}
	}

	return true
}

// sortUserList sorts the user list based on the sort parameter
func sortUserList(userList []AdminUserListItem, sortBy string) {
	switch sortBy {
	case "id_asc":
		sort.Slice(userList, func(i, j int) bool {
			return userList[i].DBUser.ID < userList[j].DBUser.ID
		})
	case "id_desc":
		sort.Slice(userList, func(i, j int) bool {
			return userList[i].DBUser.ID > userList[j].DBUser.ID
		})
	case "balance_asc":
		sort.Slice(userList, func(i, j int) bool {
			return userList[i].Balance < userList[j].Balance
		})
	case "balance_desc":
		sort.Slice(userList, func(i, j int) bool {
			return userList[i].Balance > userList[j].Balance
		})
	default:
		// Default: sort by ID descending (newest members first)
		sort.Slice(userList, func(i, j int) bool {
			return userList[i].DBUser.ID > userList[j].DBUser.ID
		})
	}
}

// AdminUsersAPIHandler returns JSON list of users with Keycloak info
// GET /api/admin/users
func (h *Handler) AdminUsersAPIHandler(w http.ResponseWriter, r *http.Request) {
	_ = r // auth enforced by RequireAdmin middleware

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

	// Batch-fetch all user balances in one query (avoids N+1)
	balanceMap, err := h.getUserBalanceMap(ctx)
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Balance error: %v", err), http.StatusInternalServerError)
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
		KeycloakLinked   bool     `json:"keycloak_linked"`
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
			Balance:  balanceMap[dbUser.ID],
		}

		// Keycloak info
		if dbUser.KeycloakID.Valid && dbUser.KeycloakID.String != "" {
			userResp.KeycloakID = dbUser.KeycloakID.String

			if kcUser, found := keycloakUsers[dbUser.KeycloakID.String]; found {
				userResp.KeycloakLinked = true
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


// fetchAllKeycloakUsers fetches all users from Keycloak API with pagination and returns them as a map.
// Keycloak defaults to max=100 per page, so we paginate to avoid silently missing users.
func (h *Handler) fetchAllKeycloakUsers(ctx context.Context, accessToken string) (map[string]KeycloakUserInfo, error) {
	baseURL := fmt.Sprintf("%s/admin/realms/%s/users", h.config.KeycloakURL, h.config.KeycloakRealm)

	const pageSize = 100
	userMap := make(map[string]KeycloakUserInfo)

	for offset := 0; ; offset += pageSize {
		reqURL := fmt.Sprintf("%s?first=%d&max=%d", baseURL, offset, pageSize)

		req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("keycloak API error: %s - %s", resp.Status, string(body))
		}

		var page []KeycloakUserInfo
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		for _, user := range page {
			userMap[user.ID] = user
		}

		// Last page: fewer results than page size means we've fetched all users
		if len(page) < pageSize {
			break
		}
	}

	return userMap, nil
}
