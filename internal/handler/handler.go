package handler

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"

	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/db"
)

// Handler holds dependencies for HTTP handlers
type Handler struct {
	auth      *auth.Authenticator
	queries   *db.Queries
	templates *template.Template
}

// New creates a new Handler instance
func New(authenticator *auth.Authenticator, database *sql.DB, templatesDir string) (*Handler, error) {
	queries := db.New(database)

	// Note: templates is set to nil, we'll parse on each request
	// This is simpler than managing template name conflicts
	return &Handler{
		auth:      authenticator,
		queries:   queries,
		templates: nil, // Will be loaded per-request
	}, nil
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
	log := func(msg string) {
		fmt.Printf("[getOrCreateUser] KC_ID=%s Email=%s - %s\n", kcUser.ID, kcUser.Email, msg)
	}

	// Try to find by Keycloak ID first
	dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: kcUser.ID, Valid: true})
	if err == nil {
		log("Found by Keycloak ID")
		return &dbUser, nil
	}
	if err != sql.ErrNoRows {
		log(fmt.Sprintf("Error finding by KC_ID: %v", err))
		return nil, err
	}

	// Try to find by email (for migration from old system)
	log("Not found by KC_ID, trying email...")
	dbUser, err = h.queries.GetUserByEmail(ctx, kcUser.Email)
	if err == nil {
		log("Found by email! Linking Keycloak ID...")
		// Found by email! Link the Keycloak ID
		linkedUser, err := h.queries.LinkKeycloakID(ctx, db.LinkKeycloakIDParams{
			KeycloakID: sql.NullString{String: kcUser.ID, Valid: true},
			Email:      kcUser.Email,
		})
		if err != nil {
			log(fmt.Sprintf("Error linking KC_ID: %v", err))
			return nil, err
		}
		log("Successfully linked!")
		return &linkedUser, nil
	}
	if err != sql.ErrNoRows {
		log(fmt.Sprintf("Error finding by email: %v", err))
		return nil, err
	}

	// User doesn't exist - create new one
	log("User not found, creating new user...")
	newUser, err := h.queries.CreateUser(ctx, db.CreateUserParams{
		KeycloakID:        sql.NullString{String: kcUser.ID, Valid: true},
		Email:             kcUser.Email,
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
		log(fmt.Sprintf("Error creating user: %v", err))
		return nil, err
	}

	log("Successfully created new user!")
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

	// Get user's payments (empty slice if none)
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
		"Balance":  balance,
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

	data := map[string]interface{}{
		"Title":   "My Profile",
		"User":    user,
		"DBUser":  dbUser,
		"Success": r.URL.Query().Get("success") == "1",
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
