package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/base48/member-portal/internal/db"
)

// AdminLevelsHandler shows the membership levels management page.
// GET /admin/levels
func (h *Handler) AdminLevelsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		log.Printf("[Handler] failed to fetch admin DB user: %v", err)
	}

	levels, err := h.queries.ListAllLevels(ctx)
	if err != nil {
		http.Error(w, "Failed to fetch levels", http.StatusInternalServerError)
		return
	}

	// Count users per level
	counts, err := h.queries.CountUsersByLevel(ctx)
	if err != nil {
		http.Error(w, "Failed to count users", http.StatusInternalServerError)
		return
	}
	userCounts := make(map[int64]int64)
	for _, c := range counts {
		userCounts[c.LevelID] = c.Count
	}

	// Count fees per level (historical references that prevent deletion)
	feeCounts, err := h.queries.CountFeesByLevel(ctx)
	if err != nil {
		http.Error(w, "Failed to count fees", http.StatusInternalServerError)
		return
	}
	feeCountMap := make(map[int64]int64)
	for _, c := range feeCounts {
		feeCountMap[c.LevelID] = c.Count
	}

	data := map[string]interface{}{
		"Title":      "Úrovně členství",
		"User":       user,
		"DBUser":     &dbUser,
		"Levels":     levels,
		"UserCounts": userCounts,
		"FeeCounts":  feeCountMap,
	}

	h.render(w, "admin_levels.html", data)
}

// AdminCreateLevelHandler creates a new membership level.
// POST /api/admin/levels
func (h *Handler) AdminCreateLevelHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	var req struct {
		Name   string `json:"name"`
		Amount string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Amount == "" {
		h.jsonError(w, "Název a částka jsou povinné", http.StatusBadRequest)
		return
	}

	// Validate amount is a positive number
	var amount float64
	if _, err := fmt.Sscanf(req.Amount, "%f", &amount); err != nil || amount < 0 {
		h.jsonError(w, "Neplatná částka", http.StatusBadRequest)
		return
	}

	newLevel, err := h.queries.CreateLevel(ctx, db.CreateLevelParams{
		Name:   req.Name,
		Amount: req.Amount,
	})
	if err != nil {
		h.jsonError(w, "Chyba při vytváření úrovně (možná duplicitní název)", http.StatusInternalServerError)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message:   fmt.Sprintf("Created membership level: %s (%s Kč) by admin %s", newLevel.Name, newLevel.Amount, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"level_id":    newLevel.ID,
			"level_name":  newLevel.Name,
			"amount":      newLevel.Amount,
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Úroveň %s vytvořena", newLevel.Name))
}

// AdminUpdateLevelHandler updates a level's name and amount.
// POST /api/admin/levels/update
func (h *Handler) AdminUpdateLevelHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	var req struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Amount string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Amount == "" {
		h.jsonError(w, "Název a částka jsou povinné", http.StatusBadRequest)
		return
	}

	var amount float64
	if _, err := fmt.Sscanf(req.Amount, "%f", &amount); err != nil || amount < 0 {
		h.jsonError(w, "Neplatná částka", http.StatusBadRequest)
		return
	}

	// Get old values for logging
	oldLevel, err := h.queries.GetLevel(ctx, req.ID)
	if err != nil {
		h.jsonError(w, "Úroveň nenalezena", http.StatusNotFound)
		return
	}

	level, err := h.queries.UpdateLevel(ctx, db.UpdateLevelParams{
		Name:   req.Name,
		Amount: req.Amount,
		ID:     req.ID,
	})
	if err != nil {
		h.jsonError(w, "Chyba při aktualizaci úrovně (možná duplicitní název)", http.StatusInternalServerError)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message: fmt.Sprintf("Updated level: %s (%s Kč) -> %s (%s Kč) by admin %s",
			oldLevel.Name, oldLevel.Amount, level.Name, level.Amount, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"level_id":       level.ID,
			"old_name":       oldLevel.Name,
			"new_name":       level.Name,
			"old_amount":     oldLevel.Amount,
			"new_amount":     level.Amount,
			"admin_email":    adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Úroveň %s aktualizována", level.Name))
}

// AdminToggleLevelActiveHandler toggles a level's active status.
// POST /api/admin/levels/toggle
func (h *Handler) AdminToggleLevelActiveHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	var req struct {
		ID     int64 `json:"id"`
		Active bool  `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	level, err := h.queries.UpdateLevelActive(ctx, db.UpdateLevelActiveParams{
		Active: req.Active,
		ID:     req.ID,
	})
	if err != nil {
		h.jsonError(w, "Chyba při změně stavu úrovně", http.StatusInternalServerError)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	status := "deactivated"
	if req.Active {
		status = "activated"
	}
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message:   fmt.Sprintf("Level %s %s by admin %s", level.Name, status, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"level_id":    level.ID,
			"level_name":  level.Name,
			"active":      req.Active,
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Úroveň %s %s", level.Name, map[bool]string{true: "aktivována", false: "deaktivována"}[req.Active]))
}

// AdminDeleteLevelHandler deletes a membership level (only if no users assigned).
// DELETE /api/admin/levels
func (h *Handler) AdminDeleteLevelHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Check if any users are assigned to this level
	counts, err := h.queries.CountUsersByLevel(ctx)
	if err != nil {
		h.jsonError(w, "Failed to check user counts", http.StatusInternalServerError)
		return
	}
	for _, c := range counts {
		if c.LevelID == req.ID && c.Count > 0 {
			h.jsonError(w, fmt.Sprintf("Nelze smazat — %d uživatelů má tuto úroveň", c.Count), http.StatusConflict)
			return
		}
	}

	// Check if any historical fees reference this level
	feeCounts, err := h.queries.CountFeesByLevel(ctx)
	if err != nil {
		h.jsonError(w, "Failed to check fee references", http.StatusInternalServerError)
		return
	}
	for _, c := range feeCounts {
		if c.LevelID == req.ID && c.Count > 0 {
			h.jsonError(w, fmt.Sprintf("Nelze smazat — %d historických záznamů poplatků odkazuje na tuto úroveň. Použij deaktivaci.", c.Count), http.StatusConflict)
			return
		}
	}

	// Get level info for logging before deletion
	level, err := h.queries.GetLevel(ctx, req.ID)
	if err != nil {
		h.jsonError(w, "Úroveň nenalezena", http.StatusNotFound)
		return
	}

	if err := h.queries.DeleteLevel(ctx, req.ID); err != nil {
		h.jsonError(w, "Chyba při mazání úrovně", http.StatusInternalServerError)
		return
	}

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID, Valid: true,
	})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message:   fmt.Sprintf("Deleted membership level: %s (%s Kč) by admin %s", level.Name, level.Amount, adminDBUser.Email),
		Metadata: logMetadata(map[string]interface{}{
			"level_id":    level.ID,
			"level_name":  level.Name,
			"amount":      level.Amount,
			"admin_email": adminDBUser.Email,
		}),
	})

	h.jsonSuccess(w, fmt.Sprintf("Úroveň %s smazána", level.Name))
}
