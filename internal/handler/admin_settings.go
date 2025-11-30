package handler

import (
	"database/sql"
	"net/http"

	"github.com/base48/member-portal/internal/db"
)

// AdminSettingsHandler shows admin settings page
func (h *Handler) AdminSettingsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	if !user.IsAdmin() {
		http.Error(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

	ctx := r.Context()

	// Get DBUser for layout
	dbUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	// Get SMTP configuration status
	smtpConfigured := h.config.SMTPHost != "" && h.config.SMTPPort != 0

	data := map[string]interface{}{
		"Title":          "Nastavení",
		"User":           user,
		"DBUser":         dbUser,
		"SMTPConfigured": smtpConfigured,
	}

	h.render(w, "admin_settings.html", data)
}

// AdminTestEmailHandler sends test email
func (h *Handler) AdminTestEmailHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !user.IsAdmin() {
		http.Error(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse form
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	emailType := r.FormValue("type")
	recipient := r.FormValue("email")

	if recipient == "" {
		http.Error(w, "Email address is required", http.StatusBadRequest)
		return
	}

	// Get user by email for template data (use admin user if recipient not found)
	testUser, err := h.queries.GetUserByEmail(ctx, recipient)
	if err != nil {
		// Use admin user as fallback
		dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
			String: user.ID,
			Valid:  true,
		})
		if err != nil {
			http.Error(w, "Failed to get user data", http.StatusInternalServerError)
			return
		}
		testUser = dbUser
	}

	// Send appropriate test email
	emailClient := h.emailClient
	var sendErr error

	switch emailType {
	case "welcome":
		sendErr = emailClient.SendWelcome(ctx, &testUser)
	case "negative_balance":
		sendErr = emailClient.SendNegativeBalance(ctx, &testUser, -500.0)
	case "debt_warning":
		sendErr = emailClient.SendDebtWarning(ctx, &testUser, -2400.0, 1000.0)
	case "membership_suspended":
		sendErr = emailClient.SendMembershipSuspended(ctx, &testUser, "Dluh na členském příspěvku přesahuje povolený limit.")
	default:
		http.Error(w, "Invalid email type", http.StatusBadRequest)
		return
	}

	if sendErr != nil {
		// Log error
		h.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "email",
			Level:     "error",
			UserID:    sql.NullInt64{Int64: testUser.ID, Valid: true},
			Message:   "Test email failed: " + sendErr.Error(),
		})
		http.Error(w, "Failed to send test email: "+sendErr.Error(), http.StatusInternalServerError)
		return
	}

	// Log success
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "success",
		UserID:    sql.NullInt64{Int64: testUser.ID, Valid: true},
		Message:   "Test email sent: " + emailType + " to " + recipient,
	})

	// Return success
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success": true, "message": "Email odeslán na ` + recipient + `"}`))
}
