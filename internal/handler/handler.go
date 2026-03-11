package handler

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
	"github.com/base48/member-portal/internal/keycloak"
	"github.com/base48/member-portal/internal/qrpay"
)

// Handler holds dependencies for HTTP handlers
type Handler struct {
	auth           *auth.Authenticator
	queries        *db.Queries
	templates      *template.Template
	config         *config.Config
	serviceAccount *auth.ServiceAccountClient
	emailClient    *email.Client
	qrpayService   *qrpay.Service
	webRoot        string
	buildDate      string
}

// New creates a new Handler instance
func New(authenticator *auth.Authenticator, database *sql.DB, cfg *config.Config, buildDate string) (*Handler, error) {
	queries := db.New(database)

	// Initialize service account if credentials are provided
	var serviceAccount *auth.ServiceAccountClient
	if cfg.KeycloakServiceAccountClientID != "" && cfg.KeycloakServiceAccountClientSecret != "" {
		var err error
		serviceAccount, err = auth.NewServiceAccountClient(
			context.Background(),
			cfg,
			cfg.KeycloakServiceAccountClientID,
			cfg.KeycloakServiceAccountClientSecret,
		)
		if err != nil {
			fmt.Printf("⚠ WARNING: Service account initialization failed: %v\n", err)
			fmt.Println("⚠ Admin features requiring service account will be unavailable")
			// Continue without service account - it's optional
		}
	}

	// Initialize QR payment service
	qrService := qrpay.NewService(cfg.BankIBAN, cfg.BankBIC)
	if !qrService.IsConfigured() {
		fmt.Println("⚠ WARNING: BANK_IBAN not configured - QR payment codes will be unavailable")
	}

	// Initialize email client (with QR service for payment codes in emails)
	// 24h delay gives admin time to review/cancel before delivery
	emailClient := email.New(cfg, queries, qrService)
	emailClient.DefaultDelay = 24 * time.Hour

	// Note: templates is set to nil, we'll parse on each request
	// This is simpler than managing template name conflicts
	return &Handler{
		auth:           authenticator,
		queries:        queries,
		templates:      nil, // Will be loaded per-request
		config:         cfg,
		serviceAccount: serviceAccount,
		emailClient:    emailClient,
		qrpayService:   qrService,
		webRoot:        cfg.WebRoot,
		buildDate:      buildDate,
	}, nil
}

// getUserBalanceMap batch-fetches balances for all users in a single query.
// Returns a map from user ID to balance (avoids N+1 per-user GetUserBalance calls).
func (h *Handler) getUserBalanceMap(ctx context.Context) (map[int64]int64, error) {
	rows, err := h.queries.ListUserBalances(ctx)
	if err != nil {
		return nil, err
	}
	balanceMap := make(map[int64]int64, len(rows))
	for _, row := range rows {
		balanceMap[row.UserID] = row.Balance
	}
	return balanceMap, nil
}

// getServiceAccountToken is a helper to get service account token with error handling
func (h *Handler) getServiceAccountToken(ctx context.Context) (string, error) {
	if h.serviceAccount == nil {
		return "", fmt.Errorf("service account not configured")
	}
	return h.serviceAccount.GetAccessToken(ctx)
}

// HomeHandler displays the home page
func (h *Handler) HomeHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)

	data := map[string]interface{}{
		"Title": "Base48 Member Portal",
		"User":  user,
	}

	h.render(w, "home.html", data)
}

// getOrCreateUser tries to find user by Keycloak ID, then by email (for migration),
// and creates a new user if none exists
func (h *Handler) getOrCreateUser(r *http.Request, kcUser *auth.User) (*db.User, error) {
	ctx := r.Context()

	// Resolve locale from Keycloak (fallback to "cs")
	locale := kcUser.Locale
	if locale == "" {
		locale = "cs"
	}

	// Try to find by Keycloak ID first
	dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: kcUser.ID, Valid: true})
	if err == nil {
		// Sync username + locale from Keycloak on every login
		needsUpdate := dbUser.Locale != locale
		newUsername := dbUser.Username
		if kcUser.PreferredName != "" && dbUser.Username.String != kcUser.PreferredName {
			newUsername = sql.NullString{String: kcUser.PreferredName, Valid: true}
			needsUpdate = true
		}
		if needsUpdate {
			updatedUser, err := h.queries.UpdateUserKeycloakInfo(ctx, db.UpdateUserKeycloakInfoParams{
				Username: newUsername,
				Locale:   locale,
				ID:       dbUser.ID,
			})
			if err == nil {
				return &updatedUser, nil
			}
		}
		return &dbUser, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// Try to find by email (for migration from old system)
	dbUser, err = h.queries.GetUserByEmail(ctx, kcUser.Email)
	if err == nil {
		// Found by email! Link the Keycloak ID
		linkedUser, err := h.queries.LinkKeycloakID(ctx, db.LinkKeycloakIDParams{
			KeycloakID: sql.NullString{String: kcUser.ID, Valid: true},
			Email:      kcUser.Email,
		})
		if err != nil {
			return nil, err
		}

		// Log Keycloak association
		h.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "keycloak",
			Level:     "success",
			UserID:    sql.NullInt64{Int64: linkedUser.ID, Valid: true},
			Message:  fmt.Sprintf("Keycloak ID associated: %s", kcUser.Email),
			Metadata: logMetadata(map[string]interface{}{"keycloak_id": kcUser.ID, "email": kcUser.Email}),
		})

		// If claimed user is already accepted, sync active_member role
		if linkedUser.State == "accepted" {
			if token, err := h.getServiceAccountToken(ctx); err != nil {
				log.Printf("[Keycloak] Warning: cannot sync active_member after profile claim for %s: %v", linkedUser.Email, err)
			} else {
				kcClient := keycloak.NewClient(h.config, token)
				if err := kcClient.AssignRoleToUser(ctx, kcUser.ID, "active_member"); err != nil {
					log.Printf("[Keycloak] Warning: failed to assign active_member after profile claim for %s: %v", linkedUser.Email, err)
				} else {
					log.Printf("[Keycloak] Assigned active_member to %s (profile claimed)", linkedUser.Email)
					h.queries.CreateLog(ctx, db.CreateLogParams{
						Subsystem: "keycloak",
						Level:     "success",
						UserID:    sql.NullInt64{Int64: linkedUser.ID, Valid: true},
						Message:   fmt.Sprintf("Assigned active_member role (profile claimed, was already accepted)"),
					})
				}
			}
		}

		// Sync username + locale from Keycloak
		newUsername := linkedUser.Username
		if kcUser.PreferredName != "" && linkedUser.Username.String != kcUser.PreferredName {
			newUsername = sql.NullString{String: kcUser.PreferredName, Valid: true}
		}
		updatedUser, err := h.queries.UpdateUserKeycloakInfo(ctx, db.UpdateUserKeycloakInfoParams{
			Username: newUsername,
			Locale:   locale,
			ID:       linkedUser.ID,
		})
		if err == nil {
			return &updatedUser, nil
		}
		return &linkedUser, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// Auto-allocate variable symbol for the new user
	var paymentsID sql.NullString
	maxVS, err := h.queries.GetMaxNumericPaymentsID(ctx)
	if err != nil {
		log.Printf("[VS] Warning: failed to get max VS for auto-allocation: %v", err)
	} else {
		newVS := fmt.Sprintf("%d", maxVS+1)
		paymentsID = sql.NullString{String: newVS, Valid: true}
		log.Printf("[VS] Auto-allocated VS '%s' for new user %s", newVS, kcUser.Email)
	}

	// User doesn't exist - create new one
	newUser, err := h.queries.CreateUser(ctx, db.CreateUserParams{
		KeycloakID:        sql.NullString{String: kcUser.ID, Valid: true},
		Email:             strings.ToLower(kcUser.Email),
		Username:          sql.NullString{String: kcUser.PreferredName, Valid: kcUser.PreferredName != ""},
		Realname:          sql.NullString{String: kcUser.Name, Valid: kcUser.Name != ""},
		Phone:             sql.NullString{},
		AltContact:        sql.NullString{},
		LevelID:           1, // Awaiting level
		LevelActualAmount: "0",
		PaymentsID:        paymentsID,
		State:             "awaiting",
		IsCouncil:         false,
		IsStaff:           false,
		Locale:            locale,
	})
	if err != nil {
		return nil, err
	}

	// Log new user registration
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "auth",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: newUser.ID, Valid: true},
		Message:  fmt.Sprintf("New user registered: %s (VS: %s)", kcUser.Email, paymentsID.String),
		Metadata: logMetadata(map[string]interface{}{"keycloak_id": kcUser.ID, "email": kcUser.Email, "payments_id": paymentsID.String}),
	})

	// Send registration confirmation email (non-blocking, 30s timeout)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := h.emailClient.SendRegistration(ctx, &newUser); err != nil {
			log.Printf("[Email] Warning: failed to send registration email to %s: %v", newUser.Email, err)
		}
	}()

	return &newUser, nil
}

// ServicesHandler displays Keycloak-linked services/applications
func (h *Handler) ServicesHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if dbUser.State == "awaiting" && !user.IsAdmin() {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"Title":  "Služby",
		"User":   user,
		"DBUser": dbUser,
	}

	h.render(w, "services.html", data)
}

// ProfileHandler displays and updates user profile
func (h *Handler) ProfileHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if r.Method == http.MethodPost {
		// Check which form was submitted
		if r.FormValue("action") == "update_custom_fee" {
			h.handleCustomFeeUpdate(w, r, dbUser)
			return
		}
		if r.FormValue("action") == "upgrade_level" {
			h.handleLevelUpgrade(w, r, dbUser)
			return
		}

		// Update profile (member portal fields only — realname handled by Keycloak IDP)
		_, err := h.queries.UpdateUserProfile(r.Context(), db.UpdateUserProfileParams{
			Phone:      sql.NullString{String: r.FormValue("phone"), Valid: r.FormValue("phone") != ""},
			AltContact: sql.NullString{String: r.FormValue("alt_contact"), Valid: r.FormValue("alt_contact") != ""},
			ID:         dbUser.ID,
		})
		if err != nil {
			http.Error(w, "Failed to update profile", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/profile?success=1", http.StatusSeeOther)
		return
	}

	// Build profile data using shared helper
	data, err := h.buildProfileData(r.Context(), dbUser, user)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to build profile data: %v", err), http.StatusInternalServerError)
		return
	}

	// Add user-specific data
	data["Title"] = "My Profile"
	data["User"] = data["ViewedUser"]  // For own profile, ViewedUser = current user
	data["DBUser"] = dbUser             // For layout compatibility (current user)
	data["Success"] = r.URL.Query().Get("success") == "1"

	h.render(w, "profile.html", data)
}

// handleCustomFeeUpdate handles updating user's custom membership fee amount
func (h *Handler) handleCustomFeeUpdate(w http.ResponseWriter, r *http.Request, dbUser *db.User) {
	customFeeStr := r.FormValue("custom_fee_amount")

	// Parse the custom fee amount
	var customFee float64
	if _, err := fmt.Sscanf(customFeeStr, "%f", &customFee); err != nil {
		http.Error(w, "Neplatná částka", http.StatusBadRequest)
		return
	}

	// Get user's current level to validate minimum
	level, err := h.queries.GetLevel(r.Context(), dbUser.LevelID)
	if err != nil {
		http.Error(w, "Chyba při načítání úrovně členství", http.StatusInternalServerError)
		return
	}

	// Parse level minimum amount
	var levelMinimum float64
	if _, err := fmt.Sscanf(level.Amount, "%f", &levelMinimum); err != nil {
		http.Error(w, "Chyba konfigurace úrovně členství", http.StatusInternalServerError)
		return
	}

	// Validate: custom fee must be >= level minimum
	if customFee < levelMinimum {
		http.Error(w, fmt.Sprintf("Částka musí být minimálně %s Kč (minimum pro %s)", level.Amount, level.Name), http.StatusBadRequest)
		return
	}

	// Update the custom fee amount
	_, err = h.queries.UpdateUserCustomFee(r.Context(), db.UpdateUserCustomFeeParams{
		LevelActualAmount: fmt.Sprintf("%.0f", customFee),
		ID:                dbUser.ID,
	})
	if err != nil {
		http.Error(w, "Chyba při aktualizaci členského příspěvku", http.StatusInternalServerError)
		return
	}

	// Log the change
	h.queries.CreateLog(r.Context(), db.CreateLogParams{
		Subsystem: "membership",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: dbUser.ID, Valid: true},
		Message:  fmt.Sprintf("Custom fee amount updated: %.0f Kč (minimum: %s Kč)", customFee, level.Amount),
		Metadata: logMetadata(map[string]interface{}{"old_amount": dbUser.LevelActualAmount, "new_amount": fmt.Sprintf("%.0f", customFee), "level_minimum": level.Amount}),
	})

	http.Redirect(w, r, "/profile?success=1", http.StatusSeeOther)
}

// handleLevelUpgrade handles self-service membership level upgrade
func (h *Handler) handleLevelUpgrade(w http.ResponseWriter, r *http.Request, dbUser *db.User) {
	newLevelIDStr := r.FormValue("new_level_id")
	newLevelID, err := strconv.ParseInt(newLevelIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Neplatná úroveň členství", http.StatusBadRequest)
		return
	}

	// Get current level
	currentLevel, err := h.queries.GetLevel(r.Context(), dbUser.LevelID)
	if err != nil {
		http.Error(w, "Chyba při načítání aktuální úrovně", http.StatusInternalServerError)
		return
	}

	// Get target level
	newLevel, err := h.queries.GetLevel(r.Context(), newLevelID)
	if err != nil {
		http.Error(w, "Zvolená úroveň členství neexistuje", http.StatusBadRequest)
		return
	}

	if !newLevel.Active {
		http.Error(w, "Zvolená úroveň členství není aktivní", http.StatusBadRequest)
		return
	}

	// Validate: new level must have higher amount than current
	var currentAmount, newAmount float64
	fmt.Sscanf(currentLevel.Amount, "%f", &currentAmount)
	fmt.Sscanf(newLevel.Amount, "%f", &newAmount)
	if newAmount <= currentAmount {
		http.Error(w, "Lze přejít pouze na vyšší úroveň členství", http.StatusBadRequest)
		return
	}

	// Update level (resets custom fee to 0)
	_, err = h.queries.UpdateUserLevel(r.Context(), db.UpdateUserLevelParams{
		LevelID: newLevelID,
		ID:      dbUser.ID,
	})
	if err != nil {
		http.Error(w, "Chyba při změně úrovně členství", http.StatusInternalServerError)
		return
	}

	h.queries.CreateLog(r.Context(), db.CreateLogParams{
		Subsystem: "membership",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: dbUser.ID, Valid: true},
		Message:   fmt.Sprintf("Membership level upgraded: %s (%s Kč) → %s (%s Kč)", currentLevel.Name, currentLevel.Amount, newLevel.Name, newLevel.Amount),
		Metadata:  logMetadata(map[string]interface{}{"old_level_id": dbUser.LevelID, "new_level_id": newLevelID, "old_level": currentLevel.Name, "new_level": newLevel.Name}),
	})

	http.Redirect(w, r, "/profile?success=1", http.StatusSeeOther)
}

// QRPaymentHandler serves a QR payment code as PNG image.
// GET /api/qr?vs=123&amount=1000&msg=PRISPEVEK
// Public endpoint — the QR encodes only bank IBAN + VS + optional amount.
// amount is optional — when omitted, the QR code has no pre-filled amount.
func (h *Handler) QRPaymentHandler(w http.ResponseWriter, r *http.Request) {
	vs := r.URL.Query().Get("vs")
	amountStr := r.URL.Query().Get("amount")
	msg := r.URL.Query().Get("msg")

	if vs == "" {
		http.Error(w, "vs parameter required", http.StatusBadRequest)
		return
	}

	var amount float64
	if amountStr != "" {
		var err error
		amount, err = strconv.ParseFloat(amountStr, 64)
		if err != nil || amount < 0 {
			http.Error(w, "invalid amount", http.StatusBadRequest)
			return
		}
	}

	if h.qrpayService == nil || !h.qrpayService.IsConfigured() {
		http.Error(w, "QR payment service not configured", http.StatusServiceUnavailable)
		return
	}

	if msg == "" {
		msg = "CLENSKY PRISPEVEK BASE48"
	}

	png, err := h.qrpayService.GeneratePaymentPNG(qrpay.GenerateParams{
		Amount:         amount,
		VariableSymbol: vs,
		Message:        msg,
		Size:           250,
	})
	if err != nil {
		http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(png)
}

// formatNumber formats an integer with non-breaking space as thousands separator (e.g. 50186 → "50\u00a0186").
func formatNumber(v interface{}) string {
	var n int64
	switch val := v.(type) {
	case int:
		n = int64(val)
	case int64:
		n = val
	case float64:
		n = int64(val)
	default:
		return fmt.Sprintf("%v", v)
	}

	negative := n < 0
	if negative {
		n = -n
	}

	s := strconv.FormatInt(n, 10)
	// Insert non-breaking spaces from the right
	var result strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result.WriteRune('\u00a0')
		}
		result.WriteRune(c)
	}

	if negative {
		return "-" + result.String()
	}
	return result.String()
}

// render is a helper to render templates
func (h *Handler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Add global template data
	if dataMap, ok := data.(map[string]interface{}); ok {
		dataMap["BaseURL"] = h.config.BaseURL
		dataMap["BuildDate"] = h.buildDate
		dataMap["DevMode"] = h.buildDate == "dev"

		// Awaiting member message
		if msg, err := h.queries.GetSetting(context.Background(), "awaiting_message"); err == nil && msg.Value != "" {
			dataMap["AwaitingMessage"] = msg.Value
		}

		// Emergency banner
		if banner, err := h.queries.GetSetting(context.Background(), "banner_text"); err == nil {
			dataMap["BannerText"] = banner.Value
		}
		if enabled, err := h.queries.GetSetting(context.Background(), "banner_enabled"); err == nil && enabled.Value == "true" {
			dataMap["BannerEnabled"] = true
		}
		if color, err := h.queries.GetSetting(context.Background(), "banner_color"); err == nil && color.Value != "" {
			dataMap["BannerColor"] = color.Value
		} else {
			dataMap["BannerColor"] = "#dc2626"
		}

		// Inject Keycloak applications for navigation
		if user, ok := dataMap["User"].(*auth.User); ok && user != nil {
			dataMap["UserApplications"] = h.auth.GetUserApplications(user.ID)
		}
	}

	// Parse templates fresh each time to avoid name conflicts
	funcMap := template.FuncMap{
		"fmtNum":      formatNumber,
		"fmtCZK":      formatCentsAsCZK,
		"fmtCZKWhole": formatCentsAsWholeCZK,
	}
	tmpl, err := template.New("").Funcs(funcMap).ParseFiles(
		filepath.Join(h.webRoot, "templates", "layout.html"),
		filepath.Join(h.webRoot, "templates", name),
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template parse error: %v", err), http.StatusInternalServerError)
		return
	}

	// Execute the layout template (which includes the specific page)
	if err := tmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		http.Error(w, fmt.Sprintf("Template execution error: %v", err), http.StatusInternalServerError)
	}
}
