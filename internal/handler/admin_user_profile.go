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
	"strings"
	"time"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/keycloak"
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

	// Fetch upgrade levels (active levels with higher amount than current)
	var currentAmount float64
	fmt.Sscanf(level.Amount, "%f", &currentAmount)
	allLevels, err := h.queries.ListActiveLevels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch levels: %w", err)
	}
	var upgradeLevels []db.Level
	for _, l := range allLevels {
		var amt float64
		fmt.Sscanf(l.Amount, "%f", &amt)
		if amt > currentAmount {
			upgradeLevels = append(upgradeLevels, l)
		}
	}

	data := map[string]interface{}{
		"ViewedUser":         targetUser,    // The user being viewed (renamed for clarity)
		"TargetDBUser":       targetDBUser,  // The user being viewed (DB record)
		"Level":              level,
		"UpgradeLevels":      upgradeLevels,
		"AllActiveLevels":    allLevels,
		"Payments":           displayPayments, // Filtered: only payments >= 5 Kč
		"Fees":               fees,
		"Balance":            float64(balance),
		"TotalPaid":          int64(totalPaid),
		"KeycloakAccountURL": keycloakAccountURL,
		"IsAdminView":        false, // Default, will be overridden if admin view
		"PaymentQRCode":      template.URL(paymentQRCode), // Mark as safe URL for template
		"QRAmount":           qrAmount,
	}

	// RevBank bar balance (if user has a username)
	if targetDBUser.Username.Valid && targetDBUser.Username.String != "" {
		revbankAccount, err := h.queries.GetRevbankAccountByUsername(ctx, strings.ToLower(targetDBUser.Username.String))
		if err == nil {
			data["RevbankAccount"] = revbankAccount
			txns, _ := h.queries.ListRevbankTransactionsByUsername(ctx, db.ListRevbankTransactionsByUsernameParams{
				Username: strings.ToLower(targetDBUser.Username.String),
				Limit:    50,
			})
			data["RevbankTransactions"] = txns
		}
	}

	return data, nil
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

	// Sync active_member Keycloak role on state transitions
	if targetUser.KeycloakID.Valid && targetUser.KeycloakID.String != "" {
		keycloakID := targetUser.KeycloakID.String
		if req.State == "accepted" && previousState != "accepted" {
			// Transitioning TO accepted → assign active_member
			if token, err := h.getServiceAccountToken(ctx); err != nil {
				log.Printf("[Keycloak] Warning: cannot sync active_member for %s: %v", updatedUser.Email, err)
			} else {
				kcClient := keycloak.NewClient(h.config, token)
				if err := kcClient.AssignRoleToUser(ctx, keycloakID, "active_member"); err != nil {
					log.Printf("[Keycloak] Warning: failed to assign active_member to %s: %v", updatedUser.Email, err)
				} else {
					log.Printf("[Keycloak] Assigned active_member to %s", updatedUser.Email)
					h.queries.CreateLog(ctx, db.CreateLogParams{
						Subsystem: "keycloak",
						Level:     "success",
						UserID:    sql.NullInt64{Int64: userID, Valid: true},
						Message:   fmt.Sprintf("Assigned active_member role (state changed to accepted)"),
					})
				}
			}
		} else if req.State != "accepted" && previousState == "accepted" {
			// Transitioning FROM accepted → remove active_member
			if token, err := h.getServiceAccountToken(ctx); err != nil {
				log.Printf("[Keycloak] Warning: cannot sync active_member for %s: %v", updatedUser.Email, err)
			} else {
				kcClient := keycloak.NewClient(h.config, token)
				if err := kcClient.RemoveRoleFromUser(ctx, keycloakID, "active_member"); err != nil {
					log.Printf("[Keycloak] Warning: failed to remove active_member from %s: %v", updatedUser.Email, err)
				} else {
					log.Printf("[Keycloak] Removed active_member from %s", updatedUser.Email)
					h.queries.CreateLog(ctx, db.CreateLogParams{
						Subsystem: "keycloak",
						Level:     "success",
						UserID:    sql.NullInt64{Int64: userID, Valid: true},
						Message:   fmt.Sprintf("Removed active_member role (state changed to %s)", req.State),
					})
				}
			}
		}
	}

	// Send welcome email on transition TO "accepted"
	if req.State == "accepted" && previousState != "accepted" {
		if err := h.emailClient.SendWelcome(ctx, &updatedUser); err != nil {
			// Don't fail the state change because of email failure
			log.Printf("[Email] Warning: failed to send welcome email to %s: %v", updatedUser.Email, err)
		}
	}

	// Send suspended email on transition TO "suspended"
	if req.State == "suspended" && previousState != "suspended" {
		if err := h.emailClient.SendMembershipSuspended(ctx, &updatedUser, ""); err != nil {
			log.Printf("[Email] Warning: failed to send suspended email to %s: %v", updatedUser.Email, err)
		}
	}

	h.jsonSuccess(w, fmt.Sprintf("Stav změněn na %s", req.State))
}

// AdminUpdateUserLevelHandler changes a user's membership level.
// POST /api/admin/users/{id}/level
func (h *Handler) AdminUpdateUserLevelHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		LevelID int64 `json:"level_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate the new level exists and is active
	newLevel, err := h.queries.GetLevel(ctx, req.LevelID)
	if err != nil {
		h.jsonError(w, "Zvolená úroveň neexistuje", http.StatusBadRequest)
		return
	}
	if !newLevel.Active {
		h.jsonError(w, "Zvolená úroveň není aktivní", http.StatusBadRequest)
		return
	}

	// Get current user for logging
	targetUser, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	previousLevel, _ := h.queries.GetLevel(ctx, targetUser.LevelID)

	_, err = h.queries.UpdateUserLevel(ctx, db.UpdateUserLevelParams{
		LevelID: req.LevelID,
		ID:      userID,
	})
	if err != nil {
		h.jsonError(w, "Failed to update level", http.StatusInternalServerError)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message: fmt.Sprintf("Level changed: %s (%s Kč) -> %s (%s Kč) (by admin %s)",
			previousLevel.Name, previousLevel.Amount, newLevel.Name, newLevel.Amount, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"previous_level_id": targetUser.LevelID,
			"new_level_id":     req.LevelID,
			"previous_level":   previousLevel.Name,
			"new_level":        newLevel.Name,
			"admin_email":      adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Úroveň změněna na %s", newLevel.Name))
}

// AdminAllocateUserVSHandler allocates or sets a variable symbol for a user's membership payments.
// POST /api/admin/users/{id}/vs
// Body: {"vs": "751"} for manual, {"auto": true} for auto-allocation
func (h *Handler) AdminAllocateUserVSHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		VS   string `json:"vs"`
		Auto bool   `json:"auto"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Get target user
	targetUser, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	vs := req.VS

	// Auto-allocate: find max numeric VS and increment
	if req.Auto || vs == "" {
		maxVS, err := h.queries.GetMaxNumericPaymentsID(ctx)
		if err != nil {
			log.Printf("[VS] ERROR: Failed to get max numeric VS: %v", err)
			h.jsonError(w, "Nepodařilo se zjistit další volný VS", http.StatusInternalServerError)
			return
		}
		vs = fmt.Sprintf("%d", maxVS+1)
	}

	// Validate: check for duplicate with another user
	existingUser, err := h.queries.GetUserByPaymentsID(ctx, sql.NullString{String: vs, Valid: true})
	if err == nil && existingUser.ID != userID {
		msg := fmt.Sprintf("VS '%s' je již přiřazen uživateli %s (ID %d)", vs, existingUser.Email, existingUser.ID)
		log.Printf("[VS] CONFLICT: %s — attempted by admin for user ID %d", msg, userID)
		h.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "admin",
			Level:     "error",
			UserID:    sql.NullInt64{Int64: userID, Valid: true},
			Message:   fmt.Sprintf("VS allocation CONFLICT: %s", msg),
		})
		h.jsonError(w, msg, http.StatusConflict)
		return
	}

	// Validate: check for duplicate with a project VS
	existingProjectVS, err := h.queries.GetProjectVSByVS(ctx, vs)
	if err == nil {
		msg := fmt.Sprintf("VS '%s' je již použit projektem (project_id %d)", vs, existingProjectVS.ProjectID)
		log.Printf("[VS] CONFLICT: %s — attempted by admin for user ID %d", msg, userID)
		h.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "admin",
			Level:     "error",
			UserID:    sql.NullInt64{Int64: userID, Valid: true},
			Message:   fmt.Sprintf("VS allocation CONFLICT: %s", msg),
		})
		h.jsonError(w, msg, http.StatusConflict)
		return
	}

	// Save the VS
	previousVS := ""
	if targetUser.PaymentsID.Valid {
		previousVS = targetUser.PaymentsID.String
	}

	_, err = h.queries.UpdateUserPaymentsID(ctx, db.UpdateUserPaymentsIDParams{
		PaymentsID: sql.NullString{String: vs, Valid: true},
		ID:         userID,
	})
	if err != nil {
		log.Printf("[VS] ERROR: Failed to update VS for user %d: %v", userID, err)
		h.jsonError(w, "Nepodařilo se uložit VS: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log success
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	logMsg := fmt.Sprintf("VS allocated: '%s' for user %s (by admin %s)", vs, targetUser.Email, adminDBUser.Email)
	if previousVS != "" {
		logMsg = fmt.Sprintf("VS changed: '%s' -> '%s' for user %s (by admin %s)", previousVS, vs, targetUser.Email, adminDBUser.Email)
	}
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "success",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message:   logMsg,
		Metadata: logMetadata(map[string]interface{}{
			"previous_vs": previousVS,
			"new_vs":      vs,
			"auto":        req.Auto || req.VS == "",
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("VS '%s' přiřazen", vs))
}

// AdminUpdateUserEmailHandler changes a user's email address.
// Only allowed for users NOT linked to Keycloak (legacy migration use case).
// POST /api/admin/users/{id}/email
func (h *Handler) AdminUpdateUserEmailHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" {
		h.jsonError(w, "Email je povinný", http.StatusBadRequest)
		return
	}

	// Get target user to verify and log
	targetUser, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	// Enforce: only for users not linked to Keycloak
	if targetUser.KeycloakID.Valid && targetUser.KeycloakID.String != "" {
		h.jsonError(w, "Nelze měnit email uživateli propojenému s Keycloakem. Změňte email v Keycloaku.", http.StatusForbidden)
		return
	}

	// Check email uniqueness
	existingUser, err := h.queries.GetUserByEmail(ctx, req.Email)
	if err == nil && existingUser.ID != userID {
		h.jsonError(w, fmt.Sprintf("Email '%s' je již používán jiným uživatelem (ID %d)", req.Email, existingUser.ID), http.StatusConflict)
		return
	}

	previousEmail := targetUser.Email

	updatedUser, err := h.queries.UpdateUserEmail(ctx, db.UpdateUserEmailParams{
		Email: req.Email,
		ID:    userID,
	})
	if err != nil {
		h.jsonError(w, "Nepodařilo se změnit email: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = updatedUser

	// Log the change
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "warning",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message: fmt.Sprintf("Email changed: '%s' -> '%s' (legacy migration, by admin %s)",
			previousEmail, req.Email, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"previous_email": previousEmail,
			"new_email":      req.Email,
			"admin_email":    adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Email změněn na %s", req.Email))
}

// AdminDeleteFeeHandler deletes a fee record for a user
// DELETE /api/admin/users/{id}/fees/{feeId}
func (h *Handler) AdminDeleteFeeHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	feeIDStr := chi.URLParam(r, "feeId")
	feeID, err := strconv.ParseInt(feeIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid fee ID", http.StatusBadRequest)
		return
	}

	// Verify fee exists and belongs to this user
	fee, err := h.queries.GetFee(ctx, feeID)
	if err != nil {
		h.jsonError(w, "Fee not found", http.StatusNotFound)
		return
	}
	if fee.UserID != userID {
		h.jsonError(w, "Fee does not belong to this user", http.StatusBadRequest)
		return
	}

	// Delete the fee
	if err := h.queries.DeleteFee(ctx, feeID); err != nil {
		h.jsonError(w, "Failed to delete fee: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log the action
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	targetUser, _ := h.queries.GetUserByID(ctx, userID)
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "warning",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message: fmt.Sprintf("Fee deleted: %s Kč for period %s (user: %s, by admin: %s)",
			fee.Amount, fee.PeriodStart.Format("2006-01"), targetUser.Email, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"fee_id":      feeID,
			"fee_amount":  fee.Amount,
			"period":      fee.PeriodStart.Format("2006-01"),
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Fee %s Kč za %s smazán", fee.Amount, fee.PeriodStart.Format("01/2006")))
}

// AdminCreateFeeHandler creates a manual fee record for a user
// POST /api/admin/users/{id}/fees
func (h *Handler) AdminCreateFeeHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	userIDStr := chi.URLParam(r, "id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		PeriodStart string `json:"period_start"` // "2026-03" format
		Amount      string `json:"amount"`        // e.g. "1000" or "0"
		LevelID     int64  `json:"level_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.PeriodStart == "" || req.Amount == "" || req.LevelID == 0 {
		h.jsonError(w, "Missing required fields: period_start, amount, level_id", http.StatusBadRequest)
		return
	}

	// Parse period_start "2026-03" to time.Time (first day of month)
	periodTime, err := time.Parse("2006-01", req.PeriodStart)
	if err != nil {
		h.jsonError(w, "Invalid period format, use YYYY-MM", http.StatusBadRequest)
		return
	}

	// Verify user exists
	targetUser, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	// Check if fee already exists for this period
	_, err = h.queries.GetFeeByUserAndPeriod(ctx, db.GetFeeByUserAndPeriodParams{
		UserID:      userID,
		PeriodStart: periodTime,
	})
	if err == nil {
		h.jsonError(w, fmt.Sprintf("Fee pro období %s již existuje. Nejdřív ho smažte.", req.PeriodStart), http.StatusConflict)
		return
	}

	// Verify level exists
	level, err := h.queries.GetLevel(ctx, req.LevelID)
	if err != nil {
		h.jsonError(w, "Level not found", http.StatusNotFound)
		return
	}

	// Create the fee
	newFee, err := h.queries.CreateFee(ctx, db.CreateFeeParams{
		UserID:      userID,
		LevelID:     req.LevelID,
		PeriodStart: periodTime,
		Amount:      req.Amount,
	})
	if err != nil {
		h.jsonError(w, "Failed to create fee: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log the action
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "warning",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message: fmt.Sprintf("Manual fee created: %s Kč for period %s, level %s (user: %s, by admin: %s)",
			req.Amount, req.PeriodStart, level.Name, targetUser.Email, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"fee_id":      newFee.ID,
			"fee_amount":  req.Amount,
			"level_name":  level.Name,
			"period":      req.PeriodStart,
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Fee %s Kč za %s vytvořen (úroveň: %s)", req.Amount, req.PeriodStart, level.Name))
}

// AdminUpdateUserKeysHandler toggles physical-key holder state.
// POST /api/admin/users/{id}/keys with {"action": "grant"|"return"}.
// "grant" sets keys_granted=now, keys_returned=NULL (re-issue clears any previous return).
// "return" sets keys_returned=now, leaving keys_granted intact as historical record.
func (h *Handler) AdminUpdateUserKeysHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware
	ctx := r.Context()

	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	target, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	holdsKeys := target.KeysGranted.Valid && !target.KeysReturned.Valid

	var (
		updated db.User
		message string
	)
	switch req.Action {
	case "grant":
		if holdsKeys {
			h.jsonError(w, "User already holds keys", http.StatusConflict)
			return
		}
		updated, err = h.queries.GrantUserKeys(ctx, userID)
		message = "Klíče vydány"
	case "return":
		if !holdsKeys {
			h.jsonError(w, "User does not hold keys", http.StatusConflict)
			return
		}
		updated, err = h.queries.ReturnUserKeys(ctx, userID)
		message = "Klíče vráceny"
	default:
		h.jsonError(w, "Invalid action (expected 'grant' or 'return')", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to update keys", http.StatusInternalServerError)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: user.ID, Valid: true})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "keys",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message:   fmt.Sprintf("Keys %s for %s (by admin %s)", req.Action, updated.Email, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"action":      req.Action,
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, message)
}
