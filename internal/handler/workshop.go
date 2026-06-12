package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/base48/member-portal/internal/db"
)

// reservationTimeLayout is the storage format for reservation times (local time).
// TEXT comparison in SQLite works lexicographically with this format —
// never bind time.Time directly (see migration 012).
const reservationTimeLayout = "2006-01-02 15:04"

// WorkshopResourceResponse is a bookable resource as seen by members.
type WorkshopResourceResponse struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	State         string `json:"state"`
	BlockedReason string `json:"blocked_reason"`
}

// WorkshopReservationResponse is an active reservation as seen by members.
type WorkshopReservationResponse struct {
	ID         int64  `json:"id"`
	ResourceID int64  `json:"resource_id"`
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	StartsAt   string `json:"starts_at"`
	EndsAt     string `json:"ends_at"`
	Note       string `json:"note"`
	Mine       bool   `json:"mine"`
}

// parseReservationTime accepts datetime-local input ("2006-01-02T15:04")
// and returns the storage format.
func parseReservationTime(s string) (string, error) {
	t, err := time.Parse("2006-01-02T15:04", s)
	if err != nil {
		return "", err
	}
	return t.Format(reservationTimeLayout), nil
}

// WorkshopHandler shows the workshop reservations page for authenticated members.
// GET /workshop
func (h *Handler) WorkshopHandler(w http.ResponseWriter, r *http.Request) {
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

	if dbUser.State == "awaiting" {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
		return
	}

	data := map[string]interface{}{
		"Title":  "Autodílna",
		"User":   user,
		"DBUser": dbUser,
	}

	h.render(w, "workshop.html", data)
}

// WorkshopAPIHandler returns resources and active reservations (JSON).
// GET /api/member/workshop
func (h *Handler) WorkshopAPIHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := h.auth.GetUser(r)
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	resources, err := h.queries.ListResources(ctx)
	if err != nil {
		h.jsonError(w, "Failed to fetch resources", http.StatusInternalServerError)
		return
	}

	now := time.Now().Format(reservationTimeLayout)
	reservations, err := h.queries.ListActiveReservations(ctx, now)
	if err != nil {
		h.jsonError(w, "Failed to fetch reservations", http.StatusInternalServerError)
		return
	}

	resourceResponses := make([]WorkshopResourceResponse, len(resources))
	for i, res := range resources {
		resourceResponses[i] = WorkshopResourceResponse{
			ID:            res.ID,
			Slug:          res.Slug,
			Name:          res.Name,
			Description:   res.Description.String,
			State:         res.State,
			BlockedReason: res.BlockedReason.String,
		}
	}

	reservationResponses := make([]WorkshopReservationResponse, len(reservations))
	for i, res := range reservations {
		username := res.Username.String
		if username == "" {
			username = res.Email
		}
		reservationResponses[i] = WorkshopReservationResponse{
			ID:         res.ID,
			ResourceID: res.ResourceID,
			UserID:     res.UserID,
			Username:   username,
			StartsAt:   res.StartsAt,
			EndsAt:     res.EndsAt,
			Note:       res.Note.String,
			Mine:       res.UserID == dbUser.ID,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":      true,
		"now":          now,
		"resources":    resourceResponses,
		"reservations": reservationResponses,
	})
}

// CreateReservationHandler creates a new reservation for the current member.
// POST /api/member/workshop/reservations
func (h *Handler) CreateReservationHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := h.auth.GetUser(r)
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	if dbUser.State != "accepted" {
		h.jsonError(w, "Rezervace jsou dostupné pouze přijatým členům", http.StatusForbidden)
		return
	}

	var req struct {
		ResourceID int64  `json:"resource_id"`
		StartsAt   string `json:"starts_at"`
		EndsAt     string `json:"ends_at"`
		Note       string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
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

	now := time.Now().Format(reservationTimeLayout)
	if endsAt <= startsAt {
		h.jsonError(w, "Konec rezervace musí být po začátku", http.StatusBadRequest)
		return
	}
	if endsAt <= now {
		h.jsonError(w, "Rezervace nesmí končit v minulosti", http.StatusBadRequest)
		return
	}

	reservation, err := h.queries.CreateReservation(ctx, db.CreateReservationParams{
		ResourceID: req.ResourceID,
		UserID:     dbUser.ID,
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
		UserID:    sql.NullInt64{Int64: dbUser.ID, Valid: true},
		Message:   fmt.Sprintf("Reservation created: resource %d, %s — %s", reservation.ResourceID, reservation.StartsAt, reservation.EndsAt),
		Metadata:  logMetadata(map[string]interface{}{"reservation_id": reservation.ID, "resource_id": reservation.ResourceID, "note": req.Note}),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "id": reservation.ID})
}

// CancelReservationHandler cancels member's own active reservation.
// POST /api/member/workshop/reservations/{id}/cancel
func (h *Handler) CancelReservationHandler(w http.ResponseWriter, r *http.Request) {
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

	reservation, err := h.queries.CancelOwnReservation(ctx, db.CancelOwnReservationParams{
		ID:     id,
		UserID: sql.NullInt64{Int64: dbUser.ID, Valid: true},
	})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Rezervace neexistuje, není vaše nebo už není aktivní", http.StatusNotFound)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to cancel reservation", http.StatusInternalServerError)
		return
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "workshop",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: dbUser.ID, Valid: true},
		Message:   fmt.Sprintf("Reservation cancelled: resource %d, %s — %s", reservation.ResourceID, reservation.StartsAt, reservation.EndsAt),
		Metadata:  logMetadata(map[string]interface{}{"reservation_id": reservation.ID}),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// UpdateReservationEndHandler extends/shortens member's own active reservation.
// POST /api/member/workshop/reservations/{id}/end
func (h *Handler) UpdateReservationEndHandler(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		EndsAt string `json:"ends_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	endsAt, err := parseReservationTime(req.EndsAt)
	if err != nil {
		h.jsonError(w, "Neplatný konec rezervace", http.StatusBadRequest)
		return
	}

	reservation, err := h.queries.UpdateOwnReservationEnd(ctx, db.UpdateOwnReservationEndParams{
		EndsAt: endsAt,
		ID:     id,
		UserID: dbUser.ID,
	})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Nelze změnit konec — rezervace není vaše aktivní, nebo se nový termín překrývá s jinou rezervací", http.StatusConflict)
		return
	}
	if err != nil {
		h.jsonError(w, "Failed to update reservation", http.StatusInternalServerError)
		return
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "workshop",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: dbUser.ID, Valid: true},
		Message:   fmt.Sprintf("Reservation end updated: resource %d, %s — %s", reservation.ResourceID, reservation.StartsAt, reservation.EndsAt),
		Metadata:  logMetadata(map[string]interface{}{"reservation_id": reservation.ID, "new_end": endsAt}),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}
