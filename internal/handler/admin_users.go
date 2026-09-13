package handler

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

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

// userView is a one-click preset for the users list: the state, balance cut and
// ordering an admin actually asks for, instead of three dropdowns they have to
// combine correctly every time. The presets are the primary UI; the full filter
// form is the escape hatch behind them.
type userView struct {
	Key     string
	Label   string
	State   string
	Balance string // "" | "negative" | "positive"
	Sort    string
	// Totals turns on the summary row. Off for applicants, whose balances are
	// all zero — a footer reading "0 Kč" is noise, not information.
	Totals bool
}

// Declaration order is display order.
var userViews = []userView{
	{Key: "awaiting", Label: "Čekající", State: "awaiting", Sort: "id_desc"},
	{Key: "active", Label: "Aktivní", State: "accepted", Sort: "id_desc", Totals: true},
	{Key: "debt", Label: "Dlužníci", State: "accepted", Balance: "negative", Sort: "balance_asc", Totals: true},
	{Key: "suspended", Label: "Pozastavení", State: "suspended", Sort: "id_desc", Totals: true},
}

// What /admin/users shows with no query string.
const defaultUserView = "debt"

func lookupUserView(key string) (userView, bool) {
	for _, v := range userViews {
		if v.Key == key {
			return v, true
		}
	}
	return userView{}, false
}

// currentUserView names the preset whose filters are exactly the ones in effect,
// or "" when the admin has gone off-preset. Derived from the effective filters
// rather than from the `view` parameter, so the preset still lights up when the
// same combination arrives from the filter form or an old bookmarked URL.
func currentUserView(state, balance, sort, search, keycloak string) string {
	if search != "" || keycloak != "" {
		return ""
	}
	for _, v := range userViews {
		if v.State == state && v.Balance == balance && v.Sort == sort {
			return v.Key
		}
	}
	return ""
}

// userViewLink is one rendered preset tab.
type userViewLink struct {
	Label  string
	URL    string
	Count  int
	Active bool
}

// AdminUsersHandler shows admin overview of all users with Keycloak status and roles
// GET /admin/users
func (h *Handler) AdminUsersHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	ctx := r.Context()

	query := r.URL.Query()
	filterState := query.Get("state")
	filterKeycloak := query.Get("keycloak")
	filterBalance := query.Get("balance")
	filterSearch := strings.ToLower(query.Get("search"))
	sortBy := query.Get("sort")

	// A preset overrides state/balance/sort but leaves search and Keycloak
	// alone, so "Dlužníci" can still be narrowed by a search box.
	viewKey := query.Get("view")
	if len(query) == 0 {
		viewKey = defaultUserView
	}
	if v, ok := lookupUserView(viewKey); ok {
		filterState, filterBalance, sortBy = v.State, v.Balance, v.Sort
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

	// Preset tabs, each carrying how many users it would show. The count is what
	// makes them worth glancing at — "Čekající 3" is the whole reason to click.
	// Counted over every user, not the filtered list, so a tab never reports the
	// effect of the tab you are already on.
	counts := make(map[string]int, len(userViews))
	for _, dbUser := range dbUsers {
		balance := balanceMap[dbUser.ID]
		for _, v := range userViews {
			if v.State != dbUser.State {
				continue
			}
			if (v.Balance == "negative" && balance >= 0) || (v.Balance == "positive" && balance < 0) {
				continue
			}
			counts[v.Key]++
		}
	}

	active := currentUserView(filterState, filterBalance, sortBy, filterSearch, filterKeycloak)
	views := make([]userViewLink, 0, len(userViews))
	for _, v := range userViews {
		views = append(views, userViewLink{
			Label:  v.Label,
			URL:    "/admin/users?view=" + v.Key,
			Count:  counts[v.Key],
			Active: v.Key == active,
		})
	}

	// Summary row over what is actually on screen, so it answers "how much do
	// these people owe" for whatever list the admin has narrowed to. Net and
	// debt are tracked separately: on a mixed list a net near zero can still
	// hide a large amount owed.
	var totalBalance, totalDebt int64
	debtorCount := 0
	for _, item := range userList {
		totalBalance += item.Balance
		if item.Balance < 0 {
			totalDebt += item.Balance
			debtorCount++
		}
	}

	// An off-preset list gets totals too; only the applicants view opts out.
	showTotals := true
	if v, ok := lookupUserView(active); ok {
		showTotals = v.Totals
	}

	// Render template
	data := map[string]interface{}{
		"Title":          "Admin - Users",
		"User":           user,
		"UserList":       userList,
		"UserViews":      views,
		"CustomView":     active == "",
		"ShowTotals":     showTotals && len(userList) > 0,
		"TotalBalance":   totalBalance,
		"TotalDebt":      totalDebt,
		"DebtorCount":    debtorCount,
		"AllDebtors":     debtorCount == len(userList),
		"FilterState":    filterState,
		"FilterKeycloak": filterKeycloak,
		"FilterBalance":  filterBalance,
		"FilterSearch":   query.Get("search"), // Original case
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
// GET /api/admin/users?search=query
func (h *Handler) AdminUsersAPIHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	searchQuery := strings.ToLower(r.URL.Query().Get("search"))

	// Get all users from database
	dbUsers, err := h.queries.ListUsers(ctx)
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		return
	}

	// Build response
	type UserResponse struct {
		ID               int64    `json:"id"`
		Email            string   `json:"email"`
		Username         string   `json:"username"`
		Realname         string   `json:"realname"`
		State            string   `json:"state"`
		Balance          int64    `json:"balance"`
		PaymentsID       string   `json:"payments_id"`
		KeycloakID       string   `json:"keycloak_id"`
		KeycloakLinked   bool     `json:"keycloak_linked"`
		KeycloakUsername string   `json:"keycloak_username"`
		Roles            []string `json:"roles"`
	}

	response := make([]UserResponse, 0)

	for _, dbUser := range dbUsers {
		// Apply search filter
		if searchQuery != "" {
			emailMatch := strings.Contains(strings.ToLower(dbUser.Email), searchQuery)
			nameMatch := dbUser.Realname.Valid && strings.Contains(strings.ToLower(dbUser.Realname.String), searchQuery)
			usernameMatch := dbUser.Username.Valid && strings.Contains(strings.ToLower(dbUser.Username.String), searchQuery)
			paymentsIDMatch := dbUser.PaymentsID.Valid && strings.Contains(strings.ToLower(dbUser.PaymentsID.String), searchQuery)
			if !emailMatch && !nameMatch && !usernameMatch && !paymentsIDMatch {
				continue
			}
		}

		userResp := UserResponse{
			ID:       dbUser.ID,
			Email:    dbUser.Email,
			Username: dbUser.Username.String,
			Realname: dbUser.Realname.String,
			State:    dbUser.State,
		}

		if dbUser.PaymentsID.Valid {
			userResp.PaymentsID = dbUser.PaymentsID.String
		}

		// Keycloak info
		if dbUser.KeycloakID.Valid && dbUser.KeycloakID.String != "" {
			userResp.KeycloakID = dbUser.KeycloakID.String
		}

		response = append(response, userResp)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"users":   response,
	})
}


// AdminUsersCSVExportHandler exports accepted+suspended users as a CSV file
// intended for general-assembly attendance sheets etc.
// GET /admin/users/export.csv
func (h *Handler) AdminUsersCSVExportHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	dbUsers, err := h.queries.ListUsers(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		return
	}

	filtered := make([]db.User, 0, len(dbUsers))
	for _, u := range dbUsers {
		if u.State == "accepted" || u.State == "suspended" {
			filtered = append(filtered, u)
		}
	}

	// Group accepted first, then suspended; within each group sort by ID ascending.
	stateRank := func(s string) int {
		if s == "accepted" {
			return 0
		}
		return 1
	}
	sort.Slice(filtered, func(i, j int) bool {
		ri, rj := stateRank(filtered[i].State), stateRank(filtered[j].State)
		if ri != rj {
			return ri < rj
		}
		return filtered[i].ID < filtered[j].ID
	})

	filename := fmt.Sprintf("base48-clenove-%s.csv", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")

	// UTF-8 BOM so Excel detects encoding correctly.
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{"Stav", "ID", "VS", "Přezdívka", "Jméno a příjmení", "E-mail", "Telefon"}
	if err := cw.Write(header); err != nil {
		return
	}

	stateCode := func(s string) string {
		switch s {
		case "accepted":
			return "A"
		case "suspended":
			return "S"
		default:
			return s
		}
	}

	for _, u := range filtered {
		row := []string{
			stateCode(u.State),
			fmt.Sprintf("%d", u.ID),
			nullStr(u.PaymentsID),
			nullStr(u.Username),
			nullStr(u.Realname),
			u.Email,
			nullStr(u.Phone),
		}
		if err := cw.Write(row); err != nil {
			return
		}
	}
}

func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
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
