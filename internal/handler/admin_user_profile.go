package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/db"
	"github.com/go-chi/chi/v5"
)

// AdminUserProfileHandler displays a user's profile (admin view - read only)
// GET /admin/users/:id
func (h *Handler) AdminUserProfileHandler(w http.ResponseWriter, r *http.Request) {
	currentUser := h.auth.GetUser(r)
	if currentUser == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	if !currentUser.IsAdmin() {
		http.Error(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

	ctx := r.Context()

	// Get target user ID from URL
	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	// Fetch target user from database
	targetDBUser, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// Get current admin's DB user for layout
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: currentUser.ID,
		Valid:  true,
	})

	// Fetch Keycloak info for target user (if linked)
	var targetKeycloakUser *auth.User
	if targetDBUser.KeycloakID.Valid && targetDBUser.KeycloakID.String != "" {
		// Get service account token
		accessToken, err := h.getServiceAccountToken(ctx)
		if err == nil {
			// Fetch user from Keycloak
			targetKeycloakUser, _ = h.fetchKeycloakUserByID(ctx, accessToken, targetDBUser.KeycloakID.String)
		}
	}

	// If no Keycloak data, create minimal User object from DB
	if targetKeycloakUser == nil {
		targetKeycloakUser = &auth.User{
			ID:            targetDBUser.KeycloakID.String,
			Email:         targetDBUser.Email,
			PreferredName: targetDBUser.Username.String,
			Roles:         []string{}, // No roles if not in Keycloak
		}
	}

	// Build profile data using shared helper
	data, err := h.buildProfileData(ctx, &targetDBUser, targetKeycloakUser)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error building profile data: %v", err), http.StatusInternalServerError)
		return
	}

	// Add admin-specific context
	data["IsAdminView"] = true
	data["User"] = currentUser                // For layout navbar (logged-in admin)
	data["DBUser"] = adminDBUser              // For layout navbar (logged-in admin)
	data["TargetUser"] = data["ViewedUser"]   // The user being viewed (rename for template)
	data["Title"] = fmt.Sprintf("Profil uživatele: %s", targetDBUser.Email)

	// Log admin action (track who viewed whose profile)
	adminUsername := "unknown"
	if adminDBUser.Username.Valid {
		adminUsername = adminDBUser.Username.String
	}
	targetUsername := "unknown"
	if targetDBUser.Username.Valid {
		targetUsername = targetDBUser.Username.String
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message: fmt.Sprintf("Admin %s (%s) viewed profile of user %s (%s)",
			adminUsername, adminDBUser.Email,
			targetUsername, targetDBUser.Email),
		Metadata: sql.NullString{
			String: fmt.Sprintf(`{"admin_user_id":%d,"admin_email":"%s","target_user_id":%d,"target_email":"%s"}`,
				adminDBUser.ID, adminDBUser.Email,
				userID, targetDBUser.Email),
			Valid: true,
		},
	})

	// Render using separate admin template (keeps logic clean and extensible)
	h.render(w, "admin_user_profile.html", data)
}

// buildProfileData is a shared helper that builds profile data for both
// regular profile view and admin user profile view
func (h *Handler) buildProfileData(ctx context.Context, targetDBUser *db.User, targetUser *auth.User) (map[string]interface{}, error) {
	// Fetch user's membership level
	level, err := h.queries.GetLevel(ctx, targetDBUser.LevelID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch level: %w", err)
	}

	// Fetch ALL user's payments
	payments, err := h.queries.ListPaymentsByUser(ctx, sql.NullInt64{Int64: targetDBUser.ID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch payments: %w", err)
	}

	// Fetch user's fees
	fees, err := h.queries.ListFeesByUser(ctx, targetDBUser.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch fees: %w", err)
	}

	// Calculate balance
	balance, err := h.queries.GetUserBalance(ctx, db.GetUserBalanceParams{
		UserID:   sql.NullInt64{Int64: targetDBUser.ID, Valid: true},
		UserID_2: targetDBUser.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to calculate balance: %w", err)
	}

	// Calculate total paid (sum of all payments)
	var totalPaid float64
	for _, payment := range payments {
		var amount float64
		fmt.Sscanf(payment.Amount, "%f", &amount)
		totalPaid += amount
	}

	// Build Keycloak account URL
	keycloakAccountURL := fmt.Sprintf("%s/realms/%s/account", h.config.KeycloakURL, h.config.KeycloakRealm)

	return map[string]interface{}{
		"ViewedUser":         targetUser,    // The user being viewed (renamed for clarity)
		"TargetDBUser":       targetDBUser,  // The user being viewed (DB record)
		"Level":              level,
		"Payments":           payments,
		"Fees":               fees,
		"Balance":            float64(balance),
		"TotalPaid":          int64(totalPaid),
		"KeycloakAccountURL": keycloakAccountURL,
		"IsAdminView":        false, // Default, will be overridden if admin view
	}, nil
}

// fetchKeycloakUserByID fetches a user from Keycloak by their ID
func (h *Handler) fetchKeycloakUserByID(ctx context.Context, accessToken, keycloakID string) (*auth.User, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", h.config.KeycloakURL, h.config.KeycloakRealm, keycloakID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak returned status %d", resp.StatusCode)
	}

	var kcUser KeycloakUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&kcUser); err != nil {
		return nil, err
	}

	// Fetch roles for this user
	roles, _ := h.fetchUserRolesFromKeycloak(ctx, accessToken, keycloakID)

	return &auth.User{
		ID:            kcUser.ID,
		Email:         kcUser.Email,
		PreferredName: kcUser.Username,
		Roles:         roles,
	}, nil
}

// fetchUserRolesFromKeycloak fetches roles for a specific user
func (h *Handler) fetchUserRolesFromKeycloak(ctx context.Context, accessToken, keycloakID string) ([]string, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm",
		h.config.KeycloakURL, h.config.KeycloakRealm, keycloakID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return []string{}, nil
	}

	var kcRoles []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&kcRoles); err != nil {
		return nil, err
	}

	roles := make([]string, len(kcRoles))
	for i, r := range kcRoles {
		roles[i] = r.Name
	}

	return roles, nil
}
