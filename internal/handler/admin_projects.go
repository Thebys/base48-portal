package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/base48/member-portal/internal/db"
)

// AdminProjectsHandler shows the projects management page
// GET /admin/projects
func (h *Handler) AdminProjectsHandler(w http.ResponseWriter, r *http.Request) {
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

	data := map[string]interface{}{
		"Title":  "Správa projektů",
		"User":   user,
		"DBUser": dbUser,
	}

	h.render(w, "admin_projects.html", data)
}

// ProjectResponse is the JSON response for a project
type ProjectResponse struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	PaymentsID  string  `json:"payments_id"`
	Description string  `json:"description"`
	TotalAmount float64 `json:"total_amount"`
}

// AdminProjectsAPIHandler returns list of projects (JSON)
// GET /api/admin/projects
func (h *Handler) AdminProjectsAPIHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		h.jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !user.IsAdmin() {
		h.jsonError(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

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
		// Get total amount for this project (by matching VS/identification)
		balanceInterface, err := h.queries.GetProjectBalance(ctx, p.ID)
		totalAmount := 0.0
		if err == nil {
			// The query returns interface{}, need to convert to float64
			if f, ok := balanceInterface.(float64); ok {
				totalAmount = f
			}
		}

		projectResponses[i] = ProjectResponse{
			ID:          p.ID,
			Name:        p.Name,
			PaymentsID:  p.PaymentsID.String,
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
	user := h.auth.GetUser(r)
	if user == nil {
		h.jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !user.IsAdmin() {
		h.jsonError(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

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
	user := h.auth.GetUser(r)
	if user == nil {
		h.jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !user.IsAdmin() {
		h.jsonError(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Project deleted successfully",
	})
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
	user := h.auth.GetUser(r)
	if user == nil {
		h.jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !user.IsAdmin() {
		h.jsonError(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

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

	// Get payments for this project
	payments, err := h.queries.GetProjectPayments(ctx, projectID)
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
