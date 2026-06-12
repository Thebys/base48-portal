package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/base48/member-portal/internal/db"
)

// AdminWorkshopHandler shows the workshop administration page.
// GET /admin/workshop
func (h *Handler) AdminWorkshopHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)

	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":  "Správa autodílny",
		"User":   user,
		"DBUser": dbUser,
	}

	h.render(w, "admin_workshop.html", data)
}

// AdminUpdateResourceHandler updates resource state (available/blocked/retired).
// POST /api/admin/workshop/resources/{id}
func (h *Handler) AdminUpdateResourceHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := h.auth.GetUser(r)
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid resource id", http.StatusBadRequest)
		return
	}

	var req struct {
		State         string `json:"state"`
		BlockedReason string `json:"blocked_reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.State != "available" && req.State != "blocked" && req.State != "retired" {
		h.jsonError(w, "Invalid state", http.StatusBadRequest)
		return
	}

	resource, err := h.queries.UpdateResourceState(ctx, db.UpdateResourceStateParams{
		State:         req.State,
		BlockedReason: sql.NullString{String: req.BlockedReason, Valid: req.BlockedReason != ""},
		ID:            id,
	})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Resource not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to update resource", http.StatusInternalServerError)
		return
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "workshop",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: dbUser.ID, Valid: true},
		Message:   fmt.Sprintf("Resource '%s' state changed to %s", resource.Name, resource.State),
		Metadata:  logMetadata(map[string]interface{}{"resource_id": resource.ID, "state": resource.State, "blocked_reason": req.BlockedReason}),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// AdminCreateReservationHandler creates a reservation on behalf of another member.
// POST /api/admin/workshop/reservations
func (h *Handler) AdminCreateReservationHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := h.auth.GetUser(r)
	adminUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	var req struct {
		ResourceID int64  `json:"resource_id"`
		UserID     int64  `json:"user_id"`
		StartsAt   string `json:"starts_at"`
		EndsAt     string `json:"ends_at"`
		Note       string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	targetUser, err := h.queries.GetUserByID(ctx, req.UserID)
	if err == sql.ErrNoRows {
		h.jsonError(w, "Člen nenalezen", http.StatusNotFound)
		return
	}
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}
	if targetUser.State != "accepted" {
		h.jsonError(w, "Rezervovat lze pouze pro přijaté členy", http.StatusBadRequest)
		return
	}

	startsAt, err := parseReservationTime(req.StartsAt)
	if err != nil {
		h.jsonError(w, "Neplatný začátek rezervace", http.StatusBadRequest)
		return
	}
	endsAt, err := parseReservationTime(req.EndsAt)
	if err != nil {
		h.jsonError(w, "Neplatný konec rezervace", http.StatusBadRequest)
		return
	}
	if endsAt <= startsAt {
		h.jsonError(w, "Konec rezervace musí být po začátku", http.StatusBadRequest)
		return
	}

	reservation, err := h.queries.CreateReservation(ctx, db.CreateReservationParams{
		ResourceID: req.ResourceID,
		UserID:     targetUser.ID,
		StartsAt:   startsAt,
		EndsAt:     endsAt,
		Note:       sql.NullString{String: req.Note, Valid: req.Note != ""},
	})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Termín se překrývá s jinou rezervací nebo stání není dostupné", http.StatusConflict)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to create reservation", http.StatusInternalServerError)
		return
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "workshop",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: targetUser.ID, Valid: true},
		Message:   fmt.Sprintf("Reservation created by admin for %s: resource %d, %s — %s", targetUser.Email, reservation.ResourceID, reservation.StartsAt, reservation.EndsAt),
		Metadata:  logMetadata(map[string]interface{}{"reservation_id": reservation.ID, "resource_id": reservation.ResourceID, "created_by": adminUser.ID, "note": req.Note}),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "id": reservation.ID})
}

// AdminBumpReservationHandler force-ends any active reservation.
// POST /api/admin/workshop/reservations/{id}/bump
func (h *Handler) AdminBumpReservationHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := h.auth.GetUser(r)
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid reservation id", http.StatusBadRequest)
		return
	}

	reservation, err := h.queries.BumpReservation(ctx, db.BumpReservationParams{
		AdminUserID: sql.NullInt64{Int64: dbUser.ID, Valid: true},
		ID:          id,
	})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Reservation not found or not active", http.StatusNotFound)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to bump reservation", http.StatusInternalServerError)
		return
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "workshop",
		Level:     "warning",
		UserID:    sql.NullInt64{Int64: reservation.UserID, Valid: true},
		Message:   fmt.Sprintf("Reservation bumped by admin: resource %d, %s — %s", reservation.ResourceID, reservation.StartsAt, reservation.EndsAt),
		Metadata:  logMetadata(map[string]interface{}{"reservation_id": reservation.ID, "bumped_by": dbUser.ID}),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}
