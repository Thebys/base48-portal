package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/base48/member-portal/internal/db"
)

// AdminProjectsHandler shows the projects management page
// GET /admin/projects
func (h *Handler) AdminProjectsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	ctx := r.Context()

	// Get DBUser for layout
	dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		log.Printf("[Handler] failed to fetch admin DB user: %v", err)
	}

	data := map[string]interface{}{
		"Title":  "Správa projektů",
		"User":   user,
		"DBUser": dbUser,
	}

	h.render(w, "admin_projects.html", data)
}

// VSInfo represents a VS identifier for a project
type VSInfo struct {
	VS   string `json:"vs"`
	Note string `json:"note"`
}

// ProjectResponse is a project as the admin page gets it.
type ProjectResponse struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	VSList      []VSInfo `json:"vs_list"` // primary first
	Description string   `json:"description"`
	Raised      float64  `json:"raised"`
	Spent       float64  `json:"spent"`
	Remaining   float64  `json:"remaining"`
}

// AdminProjectsAPIHandler returns all projects with what they raised and spent.
// GET /api/admin/projects
func (h *Handler) AdminProjectsAPIHandler(w http.ResponseWriter, r *http.Request) {
	projects, err := h.listProjectSummaries(r.Context())
	if err != nil {
		log.Printf("[Projects] list: %v", err)
		h.jsonError(w, "Failed to fetch projects", http.StatusInternalServerError)
		return
	}

	out := make([]ProjectResponse, len(projects))
	for i, p := range projects {
		out[i] = ProjectResponse{
			ID:          p.ID,
			Name:        p.Name,
			VSList:      p.VSList,
			Description: p.Description.String,
			Raised:      p.Raised,
			Spent:       p.Spent,
			Remaining:   p.Remaining(),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"projects": out,
	})
}

// CreateProjectRequest is the request body for creating a project
type CreateProjectRequest struct {
	Name        string `json:"name"`
	PaymentsID  string `json:"payments_id"`
	Description string `json:"description"`
}

// AdminCreateProjectHandler creates a project, with its primary VS if given.
// POST /api/admin/projects
func (h *Handler) AdminCreateProjectHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.PaymentsID = strings.TrimSpace(req.PaymentsID)
	if req.Name == "" {
		h.jsonError(w, "Project name is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if req.PaymentsID != "" {
		if msg, status := h.checkProjectVS(ctx, 0, req.PaymentsID); msg != "" {
			h.jsonError(w, msg, status)
			return
		}
	}

	// The project and its symbol go in together: a project whose VS did not
	// make it into project_vs would never get a payment assigned.
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.jsonError(w, "Failed to create project", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	qtx := h.queries.WithTx(tx)

	project, err := qtx.CreateProject(ctx, db.CreateProjectParams{
		Name:        req.Name,
		PaymentsID:  sql.NullString{String: req.PaymentsID, Valid: req.PaymentsID != ""},
		Description: sql.NullString{String: req.Description, Valid: req.Description != ""},
	})
	if err != nil {
		log.Printf("[Projects] create %q: %v", req.Name, err)
		h.jsonError(w, "Failed to create project (is the name taken?)", http.StatusConflict)
		return
	}
	if req.PaymentsID != "" {
		if _, err := qtx.AddProjectVS(ctx, db.AddProjectVSParams{
			ProjectID: project.ID,
			Vs:        req.PaymentsID,
			Note:      sql.NullString{String: "primary", Valid: true},
		}); err != nil {
			log.Printf("[Projects] add VS %s to new project: %v", req.PaymentsID, err)
			h.jsonError(w, "Failed to create project", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		h.jsonError(w, "Failed to create project", http.StatusInternalServerError)
		return
	}
	if req.PaymentsID != "" {
		h.assignPaymentsByVS(ctx, project.ID, req.PaymentsID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"project": project,
		"message": "Project created successfully",
	})
}

// projectVSPattern is what a VS can be: up to ten digits.
var projectVSPattern = regexp.MustCompile(`^[0-9]{1,10}$`)

// checkProjectVS says why vs cannot become a symbol of the project, or ""
// when it can. projectID is 0 for a project not created yet. A VS a member
// pays with is refused: the sync matches members first, so the project would
// never see a payment by it.
func (h *Handler) checkProjectVS(ctx context.Context, projectID int64, vs string) (msg string, status int) {
	if !projectVSPattern.MatchString(vs) {
		return "Variabilní symbol musí být 1 až 10 číslic", http.StatusBadRequest
	}
	switch _, err := h.queries.GetUserByPaymentsID(ctx, sql.NullString{String: vs, Valid: true}); {
	case err == nil:
		return "Tento VS používá člen, platby s ním by šly jemu", http.StatusConflict
	case err != sql.ErrNoRows:
		log.Printf("[Projects] check VS %s against members: %v", vs, err)
		return "Database error", http.StatusInternalServerError
	}
	switch existing, err := h.queries.GetProjectVSByVS(ctx, vs); {
	case err == nil && existing.ProjectID != projectID:
		return "Tento VS už používá jiný projekt", http.StatusConflict
	case err == nil:
		return "Projekt tento VS už má", http.StatusConflict
	case err != sql.ErrNoRows:
		log.Printf("[Projects] check VS %s against projects: %v", vs, err)
		return "Database error", http.StatusInternalServerError
	}
	return "", 0
}

// AdminDeleteProjectHandler deletes a project and its symbols. A project with
// payments assigned is refused: deleting it would leave them pointing at
// nothing and drop them from every total.
// DELETE /api/admin/projects  {"project_id": N}
func (h *Handler) AdminDeleteProjectHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	n, err := h.queries.CountProjectPayments(ctx, sql.NullInt64{Int64: req.ProjectID, Valid: true})
	if err != nil {
		log.Printf("[Projects] count payments of project %d: %v", req.ProjectID, err)
		h.jsonError(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		h.jsonError(w, fmt.Sprintf("Projekt má %d přiřazených plateb. Nejdřív je přeřaď jinam.", n), http.StatusConflict)
		return
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.jsonError(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	qtx := h.queries.WithTx(tx)
	if err := qtx.DeleteProjectVSByProject(ctx, req.ProjectID); err != nil {
		log.Printf("[Projects] delete symbols of project %d: %v", req.ProjectID, err)
		h.jsonError(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}
	if err := qtx.DeleteProject(ctx, req.ProjectID); err != nil {
		log.Printf("[Projects] delete project %d: %v", req.ProjectID, err)
		h.jsonError(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		h.jsonError(w, "Failed to delete project", http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, "Project deleted successfully")
}

// PaymentResponse is a project payment as the admin page gets it.
type PaymentResponse struct {
	ID             int64  `json:"id"`
	Date           string `json:"date"`
	Amount         string `json:"amount"` // negative for an expense
	RemoteAccount  string `json:"remote_account"`
	Identification string `json:"identification"`
	Note           string `json:"note"`    // what members see of an expense
	Comment        string `json:"comment"` // staff comment
}

// AdminProjectPaymentsHandler returns payments for a project
// GET /api/admin/projects/{id}/payments
func (h *Handler) AdminProjectPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	_ = r // auth enforced by RequireAdmin middleware

	// Parse project ID from query
	projectIDStr := r.URL.Query().Get("project_id")
	if projectIDStr == "" {
		h.jsonError(w, "Missing project_id parameter", http.StatusBadRequest)
		return
	}

	projectID, err := strconv.ParseInt(projectIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid project_id: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	payments, err := h.queries.GetProjectPayments(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil {
		h.jsonError(w, "Failed to fetch payments: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to response format
	paymentResponses := make([]PaymentResponse, len(payments))
	for i, p := range payments {
		paymentResponses[i] = PaymentResponse{
			ID:             p.ID,
			Date:           p.Date.Format("02.01.2006"),
			Amount:         p.Amount,
			RemoteAccount:  p.RemoteAccount,
			Identification: p.Identification,
			Note:           p.PublicNote.String,
			Comment:        p.StaffComment.String,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"payments": paymentResponses,
	})
}

// AddProjectVSRequest is the request body for adding a VS to a project
type AddProjectVSRequest struct {
	ProjectID int64  `json:"project_id"`
	VS        string `json:"vs"`
	Note      string `json:"note"`
}

// AdminAddProjectVSHandler adds a VS identifier to a project
// POST /api/admin/projects/vs
func (h *Handler) AdminAddProjectVSHandler(w http.ResponseWriter, r *http.Request) {
	_ = r // auth enforced by RequireAdmin middleware

	var req AddProjectVSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.VS = strings.TrimSpace(req.VS)

	ctx := r.Context()

	if msg, status := h.checkProjectVS(ctx, req.ProjectID, req.VS); msg != "" {
		h.jsonError(w, msg, status)
		return
	}

	_, err := h.queries.AddProjectVS(ctx, db.AddProjectVSParams{
		ProjectID: req.ProjectID,
		Vs:        req.VS,
		Note:      sql.NullString{String: req.Note, Valid: req.Note != ""},
	})

	if err != nil {
		h.jsonError(w, "Failed to add VS: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.assignPaymentsByVS(ctx, req.ProjectID, req.VS)

	h.jsonSuccess(w, "VS added successfully")
}

// assignPaymentsByVS books the payments carrying vs that belong to nobody yet
// to the project, e.g. those sent before the symbol was added. Logged, not
// fatal: the symbol is in place and the sync assigns new payments by it.
func (h *Handler) assignPaymentsByVS(ctx context.Context, projectID int64, vs string) {
	n, err := h.queries.AssignPaymentsByProjectVS(ctx, db.AssignPaymentsByProjectVSParams{
		ProjectID: sql.NullInt64{Int64: projectID, Valid: true},
		Vs:        vs,
	})
	if err != nil {
		log.Printf("[Projects] assign payments with VS %s to project %d: %v", vs, projectID, err)
	} else if n > 0 {
		log.Printf("[Projects] VS %s brought %d earlier payment(s) into project %d", vs, n, projectID)
	}
}

// RemoveProjectVSRequest is the request body for removing a VS from a project
type RemoveProjectVSRequest struct {
	ProjectID int64  `json:"project_id"`
	VS        string `json:"vs"`
}

// AdminRemoveProjectVSHandler removes a VS identifier from a project
// DELETE /api/admin/projects/vs
func (h *Handler) AdminRemoveProjectVSHandler(w http.ResponseWriter, r *http.Request) {
	_ = r // auth enforced by RequireAdmin middleware

	var req RemoveProjectVSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Check if project has more than one VS
	vsList, err := h.queries.ListProjectVS(ctx, req.ProjectID)
	if err != nil {
		h.jsonError(w, "Failed to check VS count: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if len(vsList) <= 1 {
		h.jsonError(w, "Cannot remove the last VS from a project", http.StatusBadRequest)
		return
	}

	// Remove VS from project
	err = h.queries.RemoveProjectVS(ctx, db.RemoveProjectVSParams{
		ProjectID: req.ProjectID,
		Vs:        req.VS,
	})

	if err != nil {
		h.jsonError(w, "Failed to remove VS: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The payments this symbol brought in leave with it.
	n, err := h.queries.UnassignPaymentsByProjectVS(ctx, db.UnassignPaymentsByProjectVSParams{
		ProjectID: sql.NullInt64{Int64: req.ProjectID, Valid: true},
		Vs:        req.VS,
	})
	if err != nil {
		log.Printf("[Projects] unassign payments with VS %s from project %d: %v", req.VS, req.ProjectID, err)
	} else if n > 0 {
		log.Printf("[Projects] removing VS %s took %d payment(s) out of project %d", req.VS, n, req.ProjectID)
	}

	h.jsonSuccess(w, "VS removed successfully")
}
