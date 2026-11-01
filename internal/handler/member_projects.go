package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/base48/member-portal/internal/access"
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
	user, dbUser, acc, ok := h.memberPage(w, r, access.Subject.Fundraising)
	if !ok {
		return
	}

	data := map[string]interface{}{
		"Title":  "Fundraising",
		"User":   user,
		"DBUser": dbUser,
		"Access": acc,
	}

	h.render(w, "member_projects.html", data)
}

// MemberProjectsAPIHandler returns a read-only list of projects (JSON).
// GET /api/member/projects
func (h *Handler) MemberProjectsAPIHandler(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.memberAPI(w, r, access.Subject.Fundraising); !ok {
		return
	}

	projects, err := h.listProjectSummaries(r.Context())
	if err != nil {
		log.Printf("[Projects] list: %v", err)
		h.jsonError(w, "Failed to fetch projects", http.StatusInternalServerError)
		return
	}

	out := make([]MemberProjectResponse, len(projects))
	for i, p := range projects {
		out[i] = MemberProjectResponse{
			ID:          p.ID,
			Name:        p.Name,
			VSList:      p.VSList,
			Description: p.Description.String,
			TotalAmount: p.Remaining(),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"projects": out,
	})
}

// MemberProjectPaymentsHandler returns payments for a project (read-only, no staff comments).
// Donors' account numbers are for members only; external users get the list
// without them.
// GET /api/member/projects/payments?project_id=X
func (h *Handler) MemberProjectPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	_, acc, ok := h.memberAPI(w, r, access.Subject.Fundraising)
	if !ok {
		return
	}

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
			Identification: p.Identification,
		}
		if acc.MemberArea() {
			paymentResponses[i].RemoteAccount = p.RemoteAccount
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"payments": paymentResponses,
	})
}
