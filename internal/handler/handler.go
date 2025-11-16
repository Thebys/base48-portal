package handler

import (
	"database/sql"
	"html/template"
	"net/http"
	"path/filepath"

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

	// Load all templates
	tmpl, err := template.ParseGlob(filepath.Join(templatesDir, "*.html"))
	if err != nil {
		return nil, err
	}

	return &Handler{
		auth:      authenticator,
		queries:   queries,
		templates: tmpl,
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

// DashboardHandler displays the member dashboard
func (h *Handler) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	// Get user from database
	dbUser, err := h.queries.GetUserByKeycloakID(r.Context(), user.ID)
	if err != nil {
		// User not in DB yet, create them
		if err == sql.ErrNoRows {
			h.render(w, "setup.html", map[string]interface{}{
				"Title": "Complete Setup",
				"User":  user,
			})
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Get user's level
	level, err := h.queries.GetLevel(r.Context(), dbUser.LevelID)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Get user's payments
	payments, err := h.queries.ListPaymentsByUser(r.Context(), sql.NullInt64{Int64: dbUser.ID, Valid: true})
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Get user's fees
	fees, err := h.queries.ListFeesByUser(r.Context(), dbUser.ID)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
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

	dbUser, err := h.queries.GetUserByKeycloakID(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
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
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}
