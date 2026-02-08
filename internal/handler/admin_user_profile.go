package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"strconv"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/qrpay"
	"github.com/go-chi/chi/v5"
)

// AdminUserProfileHandler displays a user's profile (admin view - read only)
// GET /admin/users/:id
func (h *Handler) AdminUserProfileHandler(w http.ResponseWriter, r *http.Request) {
	currentUser := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

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
	var kcInfo *KeycloakUserInfo
	if targetDBUser.KeycloakID.Valid && targetDBUser.KeycloakID.String != "" {
		// Get service account token
		accessToken, err := h.getServiceAccountToken(ctx)
		if err == nil {
			// Fetch user from Keycloak
			targetKeycloakUser, kcInfo, _ = h.fetchKeycloakUserByID(ctx, accessToken, targetDBUser.KeycloakID.String)
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
	data["KeycloakInfo"] = kcInfo             // Full Keycloak profile (may be nil)

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
		Metadata: logMetadata(map[string]interface{}{
			"admin_user_id": adminDBUser.ID,
			"admin_email":   adminDBUser.Email,
			"target_user_id": userID,
			"target_email":   targetDBUser.Email,
		}),
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

	// Calculate total paid (sum of all payments) and filter small payments for display
	var totalPaid float64
	var displayPayments []db.Payment
	for _, payment := range payments {
		var amount float64
		fmt.Sscanf(payment.Amount, "%f", &amount)
		totalPaid += amount
		// Only show payments >= 5 Kč in the table (small amounts like interest clutter the view)
		if amount >= 5 {
			displayPayments = append(displayPayments, payment)
		}
	}

	// Build Keycloak account URL
	keycloakAccountURL := fmt.Sprintf("%s/realms/%s/account", h.config.KeycloakURL, h.config.KeycloakRealm)

	// Generate QR payment code if user has PaymentsID (variable symbol) and has debt
	var paymentQRCode string
	var qrAmount float64
	if h.qrpayService.IsConfigured() && targetDBUser.PaymentsID.Valid && targetDBUser.PaymentsID.String != "" {
		// Generate QR for debt repayment or monthly fee
		var qrMessage string

		if balance < 0 {
			// User has debt - generate QR for full debt amount
			qrAmount = math.Abs(float64(balance))
			qrMessage = "CLENSKY PRISPEVEK BASE48"
		} else {
			// No debt - generate QR for monthly fee
			var levelAmount float64
			fmt.Sscanf(level.Amount, "%f", &levelAmount)
			// Use custom amount if set and higher than level minimum
			var customAmount float64
			fmt.Sscanf(targetDBUser.LevelActualAmount, "%f", &customAmount)
			if customAmount > levelAmount {
				qrAmount = customAmount
			} else {
				qrAmount = levelAmount
			}
			qrMessage = "CLENSKY PRISPEVEK BASE48"
		}

		if qrAmount > 0 {
			qrCode, err := h.qrpayService.GeneratePaymentQR(qrpay.GenerateParams{
				Amount:         qrAmount,
				VariableSymbol: targetDBUser.PaymentsID.String,
				Message:        qrMessage,
				Size:           200,
			})
			if err == nil {
				paymentQRCode = qrCode
			}
		}
	}

	return map[string]interface{}{
		"ViewedUser":         targetUser,    // The user being viewed (renamed for clarity)
		"TargetDBUser":       targetDBUser,  // The user being viewed (DB record)
		"Level":              level,
		"Payments":           displayPayments, // Filtered: only payments >= 5 Kč
		"Fees":               fees,
		"Balance":            float64(balance),
		"TotalPaid":          int64(totalPaid),
		"KeycloakAccountURL": keycloakAccountURL,
		"IsAdminView":        false, // Default, will be overridden if admin view
		"PaymentQRCode":      template.URL(paymentQRCode), // Mark as safe URL for template
		"QRAmount":           qrAmount,
	}, nil
}

// fetchKeycloakUserByID fetches a user from Keycloak by their ID.
// Returns the auth.User (for template compatibility) and the raw KeycloakUserInfo (for attributes).
func (h *Handler) fetchKeycloakUserByID(ctx context.Context, accessToken, keycloakID string) (*auth.User, *KeycloakUserInfo, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s", h.config.KeycloakURL, h.config.KeycloakRealm, keycloakID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("keycloak returned status %d", resp.StatusCode)
	}

	var kcUser KeycloakUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&kcUser); err != nil {
		return nil, nil, err
	}

	// Fetch roles for this user
	roles, _ := h.fetchUserRolesFromKeycloak(ctx, accessToken, keycloakID)

	return &auth.User{
		ID:            kcUser.ID,
		Email:         kcUser.Email,
		GivenName:     kcUser.FirstName,
		FamilyName:    kcUser.LastName,
		PreferredName: kcUser.Username,
		Roles:         roles,
	}, &kcUser, nil
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

// AdminUpdateUserLocaleHandler changes a user's email locale.
// POST /api/admin/users/{id}/locale
func (h *Handler) AdminUpdateUserLocaleHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	ctx := r.Context()

	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	validLocales := map[string]bool{"cs": true, "en": true}
	if !validLocales[req.Locale] {
		h.jsonError(w, "Invalid locale value", http.StatusBadRequest)
		return
	}

	_, err = h.queries.UpdateUserLocale(ctx, db.UpdateUserLocaleParams{
		Locale: req.Locale,
		ID:     userID,
	})
	if err != nil {
		h.jsonError(w, "Failed to update locale", http.StatusInternalServerError)
		return
	}

	// Log the change
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message:   fmt.Sprintf("Locale changed to %s (by admin %s)", req.Locale, adminDBUser.Email),
	})

	h.jsonSuccess(w, fmt.Sprintf("Jazyk změněn na %s", req.Locale))
}

// AdminUpdateUserStateHandler changes a user's membership state.
// POST /api/admin/users/{id}/state
// When changing TO "accepted", sends welcome email.
func (h *Handler) AdminUpdateUserStateHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	ctx := r.Context()

	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	validStates := map[string]bool{
		"awaiting": true, "accepted": true, "rejected": true,
		"exmember": true, "suspended": true,
	}
	if !validStates[req.State] {
		h.jsonError(w, "Invalid state value", http.StatusBadRequest)
		return
	}

	// Get current state for transition detection
	targetUser, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	previousState := targetUser.State

	updatedUser, err := h.queries.UpdateUserState(ctx, db.UpdateUserStateParams{
		State: req.State,
		ID:    userID,
	})
	if err != nil {
		h.jsonError(w, "Failed to update state", http.StatusInternalServerError)
		return
	}

	// Log the state change
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message: fmt.Sprintf("State changed: %s -> %s (by admin %s)",
			previousState, req.State, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"previous_state": previousState,
			"new_state":      req.State,
			"admin_email":    adminDBUser.Email,
		}),
	})

	// Send welcome email on transition TO "accepted"
	if req.State == "accepted" && previousState != "accepted" {
		if err := h.emailClient.SendWelcome(ctx, &updatedUser); err != nil {
			// Don't fail the state change because of email failure
			log.Printf("[Email] Warning: failed to send welcome email to %s: %v", updatedUser.Email, err)
		}
	}

	h.jsonSuccess(w, fmt.Sprintf("Stav změněn na %s", req.State))
}
