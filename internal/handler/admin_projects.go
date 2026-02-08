package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

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

// ProjectResponse is the JSON response for a project
type ProjectResponse struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	PaymentsID  string   `json:"payments_id"`  // Primary VS (deprecated, use VSList)
	VSList      []VSInfo `json:"vs_list"`      // All VS identifiers
	Description string   `json:"description"`
	TotalAmount float64  `json:"total_amount"`
}

// AdminProjectsAPIHandler returns list of projects (JSON)
// GET /api/admin/projects
func (h *Handler) AdminProjectsAPIHandler(w http.ResponseWriter, r *http.Request) {
	_ = r // auth enforced by RequireAdmin middleware

	ctx := r.Context()

	// Get all active projects
	projects, err := h.queries.ListProjects(ctx)
	if err != nil {
		h.jsonError(w, "Failed to fetch projects: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to response format with proper string handling and calculate totals
	projectResponses := make([]ProjectResponse, len(projects))
	for i, p := range projects {
		// Get total amount for this project (by project_id or any VS in project_vs)
		balanceInterface, err := h.queries.GetProjectBalance(ctx, sql.NullInt64{Int64: p.ID, Valid: true})
		totalAmount := 0.0
		if err != nil {
			log.Printf("[Handler] failed to get project balance for project %d: %v", p.ID, err)
		} else {
			// The query returns interface{}, need to convert to float64
			if f, ok := balanceInterface.(float64); ok {
				totalAmount = f
			}
		}

		// Get all VS identifiers for this project
		vsList, err := h.queries.ListProjectVS(ctx, p.ID)
		vsInfoList := []VSInfo{}
		if err != nil {
			log.Printf("[Handler] failed to list project VS for project %d: %v", p.ID, err)
		} else {
			for _, vs := range vsList {
				vsInfoList = append(vsInfoList, VSInfo{
					VS:   vs.Vs,
					Note: vs.Note.String,
				})
			}
		}

		projectResponses[i] = ProjectResponse{
			ID:          p.ID,
			Name:        p.Name,
			PaymentsID:  p.PaymentsID.String,
			VSList:      vsInfoList,
			Description: p.Description.String,
			TotalAmount: totalAmount,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"projects": projectResponses,
	})
}

// CreateProjectRequest is the request body for creating a project
type CreateProjectRequest struct {
	Name        string `json:"name"`
	PaymentsID  string `json:"payments_id"`
	Description string `json:"description"`
}

// AdminCreateProjectHandler creates a new project
// POST /api/admin/projects
func (h *Handler) AdminCreateProjectHandler(w http.ResponseWriter, r *http.Request) {
	_ = r // auth enforced by RequireAdmin middleware

	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		h.jsonError(w, "Project name is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Create project
	project, err := h.queries.CreateProject(ctx, db.CreateProjectParams{
		Name:        req.Name,
		PaymentsID:  sql.NullString{String: req.PaymentsID, Valid: req.PaymentsID != ""},
		Description: sql.NullString{String: req.Description, Valid: req.Description != ""},
	})

	if err != nil {
		h.jsonError(w, "Failed to create project: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Add initial VS to project_vs table
	if req.PaymentsID != "" {
		_, err = h.queries.AddProjectVS(ctx, db.AddProjectVSParams{
			ProjectID: project.ID,
			Vs:        req.PaymentsID,
			Note:      sql.NullString{String: "primary", Valid: true},
		})
		if err != nil {
			// Log but don't fail - project was created
			// The VS might already exist from migration
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"project": project,
		"message": "Project created successfully",
	})
}

// AdminDeleteProjectHandler deletes a project
// DELETE /api/admin/projects/{id}
func (h *Handler) AdminDeleteProjectHandler(w http.ResponseWriter, r *http.Request) {
	_ = r // auth enforced by RequireAdmin middleware

	// Parse project ID from URL
	var req struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Delete project
	err := h.queries.DeleteProject(ctx, req.ProjectID)
	if err != nil {
		h.jsonError(w, "Failed to delete project: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, "Project deleted successfully")
}

// PaymentResponse is the JSON response for a payment
type PaymentResponse struct {
	ID            int64  `json:"id"`
	Date          string `json:"date"`
	Amount        string `json:"amount"`
	RemoteAccount string `json:"remote_account"`
	Identification string `json:"identification"`
	Message       string `json:"message"`
	Comment       string `json:"comment"`
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

	// Get payments for this project (by project_id or any VS in project_vs)
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
			Message:        "",  // Not in Payment model
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

	if req.VS == "" {
		h.jsonError(w, "VS is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Check if this VS is already used by another project
	existing, err := h.queries.GetProjectVSByVS(ctx, req.VS)
	if err == nil && existing.ProjectID != req.ProjectID {
		h.jsonError(w, "This VS is already used by another project", http.StatusConflict)
		return
	}

	// Add VS to project
	_, err = h.queries.AddProjectVS(ctx, db.AddProjectVSParams{
		ProjectID: req.ProjectID,
		Vs:        req.VS,
		Note:      sql.NullString{String: req.Note, Valid: req.Note != ""},
	})

	if err != nil {
		h.jsonError(w, "Failed to add VS: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, "VS added successfully")
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

	h.jsonSuccess(w, "VS removed successfully")
}
