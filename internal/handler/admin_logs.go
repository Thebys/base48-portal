package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

	"github.com/base48/member-portal/internal/db"
)

// AdminLogsHandler shows system logs with filtering
// GET /admin/logs
func (h *Handler) AdminLogsHandler(w http.ResponseWriter, r *http.Request) {
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

	// Get filter parameters
	subsystem := r.URL.Query().Get("subsystem")
	level := r.URL.Query().Get("level")
	userIDStr := r.URL.Query().Get("user_id")
	limitStr := r.URL.Query().Get("limit")

	// Parse user_id filter
	var userID int64
	if userIDStr != "" {
		if parsed, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			userID = parsed
		}
	}

	// Parse limit (default 100)
	limit := int64(100)
	if limitStr != "" {
		if parsed, err := strconv.ParseInt(limitStr, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	// Fetch filtered logs
	logs, err := h.queries.ListLogsFiltered(ctx, db.ListLogsFilteredParams{
		Column1:   subsystem,
		Subsystem: subsystem,
		Column3:   level,
		Level:     level,
		Column5:   userID,
		UserID: sql.NullInt64{
			Int64: userID,
			Valid: userID > 0,
		},
		Limit: limit,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		return
	}

	// Get DBUser for layout
	dbUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	data := map[string]interface{}{
		"Title":     "Systémové logy",
		"User":      user,
		"DBUser":    dbUser,
		"Logs":      logs,
		"Subsystem": subsystem,
		"Level":     level,
		"UserID":    userIDStr,
		"Limit":     limit,
	}

	h.render(w, "admin_logs.html", data)
}
