package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/base48/member-portal/internal/access"
	"github.com/base48/member-portal/internal/db"
)

// accessGrantView is one row of the "Oprávnění" section on the admin profile.
type accessGrantView struct {
	Scope     string
	Label     string
	Note      string
	GrantedBy string
	CreatedAt time.Time
}

// adminAccessData is what the admin profile shows about a user's grants: the
// ones they hold and the scopes still left to grant.
func (h *Handler) adminAccessData(ctx context.Context, userID int64) (grants []accessGrantView, available []access.ScopeInfo, err error) {
	rows, err := h.queries.ListAccessGrants(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	held := make(map[string]bool, len(rows))
	for _, row := range rows {
		held[row.Scope] = true
		grants = append(grants, accessGrantView{
			Scope:     row.Scope,
			Label:     access.Label(access.Scope(row.Scope)),
			Note:      row.Note.String,
			GrantedBy: row.GrantedByEmail.String,
			CreatedAt: row.CreatedAt,
		})
	}
	for _, info := range access.Scopes {
		if !held[string(info.Scope)] {
			available = append(available, info)
		}
	}
	return grants, available, nil
}

// AdminGrantAccessHandler grants a user access to a part of the portal.
// POST /api/admin/users/{id}/access with {"scope": "workshop", "note": "..."}
func (h *Handler) AdminGrantAccessHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware
	ctx := r.Context()

	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Scope string `json:"scope"`
		Note  string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !access.Known(req.Scope) {
		h.jsonError(w, "Neznámé oprávnění: "+req.Scope, http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(req.Note)

	target, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: user.ID, Valid: true})
	_, err = h.queries.CreateAccessGrant(ctx, db.CreateAccessGrantParams{
		UserID:    userID,
		Scope:     req.Scope,
		Note:      sql.NullString{String: note, Valid: note != ""},
		GrantedBy: sql.NullInt64{Int64: adminDBUser.ID, Valid: adminDBUser.ID != 0},
	})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Uživatel už toto oprávnění má", http.StatusConflict)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to grant access", http.StatusInternalServerError)
		return
	}

	label := access.Label(access.Scope(req.Scope))
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "access",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message:   fmt.Sprintf("Access granted: %s for %s (by admin %s)", req.Scope, target.Email, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"scope":       req.Scope,
			"note":        note,
			"state":       target.State,
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, "Přístup povolen: "+label)
}

// AdminRevokeAccessHandler takes a grant away. Reservations already made stay;
// an admin can bump them on the workshop page.
// DELETE /api/admin/users/{id}/access/{scope}
func (h *Handler) AdminRevokeAccessHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware
	ctx := r.Context()

	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid user ID", http.StatusBadRequest)
		return
	}
	scope := chi.URLParam(r, "scope")

	target, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	grant, err := h.queries.DeleteAccessGrant(ctx, db.DeleteAccessGrantParams{UserID: userID, Scope: scope})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Uživatel toto oprávnění nemá", http.StatusNotFound)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to revoke access", http.StatusInternalServerError)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: user.ID, Valid: true})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "access",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: userID, Valid: true},
		Message:   fmt.Sprintf("Access revoked: %s for %s (by admin %s)", scope, target.Email, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"scope":       scope,
			"note":        grant.Note.String,
			"granted_at":  grant.CreatedAt,
			"state":       target.State,
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, "Přístup odebrán: "+access.Label(access.Scope(scope)))
}
