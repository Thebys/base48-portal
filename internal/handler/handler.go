package handler

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"net/http"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
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
}

// New creates a new Handler instance
func New(authenticator *auth.Authenticator, database *sql.DB, cfg *config.Config, templatesDir string) (*Handler, error) {
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
	emailClient := email.New(cfg, queries, qrService)

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
	}, nil
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

	// Try to find by Keycloak ID first
	dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: kcUser.ID, Valid: true})
	if err == nil {
		// Sync username from Keycloak if it changed
		if kcUser.PreferredName != "" && dbUser.Username.String != kcUser.PreferredName {
			updatedUser, err := h.queries.UpdateUserKeycloakInfo(ctx, db.UpdateUserKeycloakInfoParams{
				Username: sql.NullString{String: kcUser.PreferredName, Valid: true},
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
			Message:   fmt.Sprintf("Keycloak ID associated: %s", kcUser.Email),
			Metadata:  sql.NullString{String: fmt.Sprintf(`{"keycloak_id":"%s","email":"%s"}`, kcUser.ID, kcUser.Email), Valid: true},
		})

		// Sync username from Keycloak (overwrite old 'ident' if different)
		if kcUser.PreferredName != "" && linkedUser.Username.String != kcUser.PreferredName {
			updatedUser, err := h.queries.UpdateUserKeycloakInfo(ctx, db.UpdateUserKeycloakInfoParams{
				Username: sql.NullString{String: kcUser.PreferredName, Valid: true},
				ID:       linkedUser.ID,
			})
			if err == nil {
				return &updatedUser, nil
			}
		}
		return &linkedUser, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// User doesn't exist - create new one
	newUser, err := h.queries.CreateUser(ctx, db.CreateUserParams{
		KeycloakID:        sql.NullString{String: kcUser.ID, Valid: true},
		Email:             kcUser.Email,
		Username:          sql.NullString{String: kcUser.PreferredName, Valid: kcUser.PreferredName != ""},
		Realname:          sql.NullString{String: kcUser.Name, Valid: kcUser.Name != ""},
		Phone:             sql.NullString{},
		AltContact:        sql.NullString{},
		LevelID:           1, // Awaiting level
		LevelActualAmount: "0",
		PaymentsID:        sql.NullString{},
		State:             "awaiting",
		IsCouncil:         false,
		IsStaff:           false,
	})
	if err != nil {
		return nil, err
	}

	// Log new user registration
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "auth",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: newUser.ID, Valid: true},
		Message:   fmt.Sprintf("New user registered: %s", kcUser.Email),
		Metadata:  sql.NullString{String: fmt.Sprintf(`{"keycloak_id":"%s","email":"%s"}`, kcUser.ID, kcUser.Email), Valid: true},
	})

	return &newUser, nil
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
			// Handle custom fee update
			h.handleCustomFeeUpdate(w, r, dbUser)
			return
		}

		// Update profile (member portal fields only)
		_, err := h.queries.UpdateUserProfile(r.Context(), db.UpdateUserProfileParams{
			Realname:   sql.NullString{String: r.FormValue("realname"), Valid: r.FormValue("realname") != ""},
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
		Message:   fmt.Sprintf("Custom fee amount updated: %.0f Kč (minimum: %s Kč)", customFee, level.Amount),
		Metadata:  sql.NullString{String: fmt.Sprintf(`{"old_amount":"%s","new_amount":"%.0f","level_minimum":"%s"}`, dbUser.LevelActualAmount, customFee, level.Amount), Valid: true},
	})

	http.Redirect(w, r, "/profile?success=1", http.StatusSeeOther)
}

// render is a helper to render templates
func (h *Handler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Add BaseURL to template data for OG tags
	if dataMap, ok := data.(map[string]interface{}); ok {
		dataMap["BaseURL"] = h.config.BaseURL
	}

	// Parse templates fresh each time to avoid name conflicts
	tmpl, err := template.ParseFiles(
		"web/templates/layout.html",
		"web/templates/"+name,
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
