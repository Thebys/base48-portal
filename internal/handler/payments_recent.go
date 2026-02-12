package handler

import (
	"fmt"
	"net/http"

	"github.com/base48/member-portal/internal/db"
)

// RecentPaymentView holds payment data for display in recent payments list.
type RecentPaymentView struct {
	db.Payment
	UserName    string // For admin view: assigned user name
	ProjectName string // For admin view: assigned project name
	IsNegative  bool   // True if amount is negative (outgoing payment)
}

// MemberRecentPaymentsHandler shows recent payments (current + last month) for members.
// Displays simplified view: date, amount, variable symbol.
// GET /payments/recent
func (h *Handler) MemberRecentPaymentsHandler(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()

	// Get all recent payments
	payments, err := h.queries.ListRecentPayments(ctx)
	if err != nil {
		http.Error(w, "Failed to load payments", http.StatusInternalServerError)
		return
	}

	// Filter to only incoming payments (amount > 0) for member view
	var incomingPayments []db.Payment
	for _, p := range payments {
		var amount float64
		if _, err := fmt.Sscanf(p.Amount, "%f", &amount); err == nil && amount > 0 {
			incomingPayments = append(incomingPayments, p)
		}
	}

	data := map[string]interface{}{
		"Title":    "Poslední platby",
		"User":     user,
		"DBUser":   dbUser,
		"Payments": incomingPayments,
	}

	h.render(w, "member_payments_recent.html", data)
}

// AdminRecentPaymentsHandler shows full bank statement for admins.
// Displays all payment details including raw JSON data.
// GET /admin/payments/recent
func (h *Handler) AdminRecentPaymentsHandler(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()

	// Get all recent payments
	payments, err := h.queries.ListRecentPayments(ctx)
	if err != nil {
		http.Error(w, "Failed to load payments", http.StatusInternalServerError)
		return
	}

	// Build user and project maps for displaying assigned payment info
	users, _ := h.queries.ListUsers(ctx)
	userMap := make(map[int64]db.User)
	for _, u := range users {
		userMap[u.ID] = u
	}

	projects, _ := h.queries.ListProjects(ctx)
	projectMap := make(map[int64]db.Project)
	for _, p := range projects {
		projectMap[p.ID] = p
	}

	// Enrich payments with user/project names
	var paymentViews []RecentPaymentView
	for _, p := range payments {
		view := RecentPaymentView{Payment: p}

		// Check if amount is negative
		var amount float64
		if _, err := fmt.Sscanf(p.Amount, "%f", &amount); err == nil && amount < 0 {
			view.IsNegative = true
		}

		if p.UserID.Valid {
			if u, ok := userMap[p.UserID.Int64]; ok {
				if u.Realname.Valid && u.Realname.String != "" {
					view.UserName = u.Realname.String
				} else if u.Username.Valid && u.Username.String != "" {
					view.UserName = u.Username.String
				} else {
					view.UserName = u.Email
				}
			}
		}

		if p.ProjectID.Valid {
			if proj, ok := projectMap[p.ProjectID.Int64]; ok {
				view.ProjectName = proj.Name
			}
		}

		paymentViews = append(paymentViews, view)
	}

	data := map[string]interface{}{
		"Title":    "Výpis plateb",
		"User":     user,
		"DBUser":   dbUser,
		"Payments": paymentViews,
	}

	h.render(w, "admin_payments_recent.html", data)
}
