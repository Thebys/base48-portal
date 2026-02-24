package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

// MemberProjectResponse is the read-only JSON response for a project visible to members.
type MemberProjectResponse struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	VSList      []VSInfo `json:"vs_list"`
	Description string   `json:"description"`
	TotalAmount float64  `json:"total_amount"`
}

// MemberPaymentResponse is a payment visible to members (no staff comments).
type MemberPaymentResponse struct {
	ID            int64  `json:"id"`
	Date          string `json:"date"`
	Amount        string `json:"amount"`
	RemoteAccount string `json:"remote_account"`
	Identification string `json:"identification"`
}

// MemberProjectsHandler shows the read-only projects/fundraising page for authenticated members.
// GET /projects
func (h *Handler) MemberProjectsHandler(w http.ResponseWriter, r *http.Request) {
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

	// Awaiting members don't have access to fundraising
	if dbUser.State == "awaiting" {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
		return
	}

	data := map[string]interface{}{
		"Title":  "Fundraising",
		"User":   user,
		"DBUser": dbUser,
	}

	h.render(w, "member_projects.html", data)
}

// MemberProjectsAPIHandler returns a read-only list of projects (JSON).
// No payment details, no CRUD operations.
// GET /api/projects
func (h *Handler) MemberProjectsAPIHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projects, err := h.queries.ListProjects(ctx)
	if err != nil {
		h.jsonError(w, "Failed to fetch projects: "+err.Error(), http.StatusInternalServerError)
		return
	}

	projectResponses := make([]MemberProjectResponse, len(projects))
	for i, p := range projects {
		balanceInterface, err := h.queries.GetProjectBalance(ctx, sql.NullInt64{Int64: p.ID, Valid: true})
		totalAmount := 0.0
		if err != nil {
			log.Printf("[Handler] failed to get project balance for project %d: %v", p.ID, err)
		} else {
			if f, ok := balanceInterface.(float64); ok {
				totalAmount = f
			}
		}

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

		projectResponses[i] = MemberProjectResponse{
			ID:          p.ID,
			Name:        p.Name,
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

// MemberProjectPaymentsHandler returns payments for a project (read-only, no staff comments).
// GET /api/member/projects/payments?project_id=X
func (h *Handler) MemberProjectPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	projectIDStr := r.URL.Query().Get("project_id")
	if projectIDStr == "" {
		h.jsonError(w, "Missing project_id parameter", http.StatusBadRequest)
		return
	}

	projectID, err := strconv.ParseInt(projectIDStr, 10, 64)
	if err != nil {
		h.jsonError(w, "Invalid project_id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	payments, err := h.queries.GetProjectPayments(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil {
		h.jsonError(w, "Failed to fetch payments", http.StatusInternalServerError)
		return
	}

	paymentResponses := make([]MemberPaymentResponse, len(payments))
	for i, p := range payments {
		paymentResponses[i] = MemberPaymentResponse{
			ID:             p.ID,
			Date:           p.Date.Format("02.01.2006"),
			Amount:         p.Amount,
			RemoteAccount:  p.RemoteAccount,
			Identification: p.Identification,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"payments": paymentResponses,
	})
}
