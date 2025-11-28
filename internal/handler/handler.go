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
)

// Handler holds dependencies for HTTP handlers
type Handler struct {
	auth           *auth.Authenticator
	queries        *db.Queries
	templates      *template.Template
	config         *config.Config
	serviceAccount *auth.ServiceAccountClient
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

	// Note: templates is set to nil, we'll parse on each request
	// This is simpler than managing template name conflicts
	return &Handler{
		auth:           authenticator,
		queries:        queries,
		templates:      nil, // Will be loaded per-request
		config:         cfg,
		serviceAccount: serviceAccount,
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

// DashboardHandler displays the member dashboard
func (h *Handler) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	// Get or create user in database
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Get user's level
	level, err := h.queries.GetLevel(r.Context(), dbUser.LevelID)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Get ALL user's payments (not just membership)
	payments, err := h.queries.ListPaymentsByUser(r.Context(), sql.NullInt64{Int64: dbUser.ID, Valid: true})
	if err != nil && err != sql.ErrNoRows {
		http.Error(w, fmt.Sprintf("Database error (payments): %v", err), http.StatusInternalServerError)
		return
	}

	// Get user's fees (empty slice if none)
	fees, err := h.queries.ListFeesByUser(r.Context(), dbUser.ID)
	if err != nil && err != sql.ErrNoRows {
		http.Error(w, fmt.Sprintf("Database error (fees): %v", err), http.StatusInternalServerError)
		return
	}

	// Calculate balance
	balance, err := h.queries.GetUserBalance(r.Context(), db.GetUserBalanceParams{
		UserID:   sql.NullInt64{Int64: dbUser.ID, Valid: true},
		UserID_2: dbUser.ID,
	})
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":    "Dashboard",
		"User":     user,
		"DBUser":   dbUser,
		"Level":    level,
		"Payments": payments,
		"Fees":     fees,
		"Balance":  float64(balance), // Convert to float64 for template comparison
	}

	h.render(w, "dashboard.html", data)
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
		// Update profile
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

	// Fetch user's membership level
	level, err := h.queries.GetLevel(r.Context(), dbUser.LevelID)
	if err != nil {
		http.Error(w, "Failed to fetch level", http.StatusInternalServerError)
		return
	}

	// Fetch ALL user's payments (not just membership)
	payments, err := h.queries.ListPaymentsByUser(r.Context(), sql.NullInt64{Int64: dbUser.ID, Valid: true})
	if err != nil {
		http.Error(w, "Failed to fetch payments", http.StatusInternalServerError)
		return
	}

	// Fetch user's fees
	fees, err := h.queries.ListFeesByUser(r.Context(), dbUser.ID)
	if err != nil {
		http.Error(w, "Failed to fetch fees", http.StatusInternalServerError)
		return
	}

	// Calculate balance
	balance, err := h.queries.GetUserBalance(r.Context(), db.GetUserBalanceParams{
		UserID:   sql.NullInt64{Int64: dbUser.ID, Valid: true},
		UserID_2: dbUser.ID,
	})
	if err != nil {
		http.Error(w, "Failed to calculate balance", http.StatusInternalServerError)
		return
	}

	// Calculate total paid (sum of all payments)
	var totalPaid float64
	for _, payment := range payments {
		// Parse amount as float
		var amount float64
		fmt.Sscanf(payment.Amount, "%f", &amount)
		totalPaid += amount
	}

	// Build Keycloak account URL
	keycloakAccountURL := fmt.Sprintf("%s/realms/%s/account", h.config.KeycloakURL, h.config.KeycloakRealm)

	data := map[string]interface{}{
		"Title":              "My Profile",
		"User":               user,
		"DBUser":             dbUser,
		"Level":              level,
		"Payments":           payments,
		"Fees":               fees,
		"Balance":            float64(balance), // Membership balance (only matching VS)
		"TotalPaid":          int64(totalPaid), // Total of ALL payments
		"Success":            r.URL.Query().Get("success") == "1",
		"KeycloakAccountURL": keycloakAccountURL,
	}

	h.render(w, "profile.html", data)
}

// render is a helper to render templates
func (h *Handler) render(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

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
