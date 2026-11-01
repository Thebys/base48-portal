package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/base48/member-portal/internal/access"
	"github.com/base48/member-portal/internal/db"
)

// RecentPaymentView holds payment data for display in recent payments list.
type RecentPaymentView struct {
	db.Payment
	UserName    string // For admin view: assigned user name
	ProjectName string // For admin view: assigned project name
	IsNegative  bool   // True if amount is negative (outgoing payment)
}

// memberRecentPayment is a bank payment as members see it: a payment in with
// its VS, or a payment out with what it was for.
type memberRecentPayment struct {
	Date     time.Time
	Value    float64
	Outgoing bool
	VS       string // incoming
	Note     string // outgoing: the note an admin approved
	Project  string // outgoing: the project it was spent on
}

// MemberRecentPaymentsHandler shows the account's payments of this and last
// month. Who an outgoing payment went to is left out; members see what it
// was for.
// GET /payments/recent
func (h *Handler) MemberRecentPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	// A sub-page of the finance overview, so the same members-only rule.
	user, dbUser, acc, ok := h.memberPage(w, r, access.Subject.MemberArea)
	if !ok {
		return
	}

	ctx := r.Context()
	payments, err := h.queries.ListRecentPayments(ctx)
	if err != nil {
		http.Error(w, "Failed to load payments", http.StatusInternalServerError)
		return
	}
	projects, err := h.queries.ListProjects(ctx)
	if err != nil {
		http.Error(w, "Failed to load payments", http.StatusInternalServerError)
		return
	}
	projectNames := make(map[int64]string, len(projects))
	for _, p := range projects {
		projectNames[p.ID] = p.Name
	}

	var rows []memberRecentPayment
	var in, out float64
	for _, p := range payments {
		value, err := strconv.ParseFloat(p.Amount, 64)
		if err != nil || value == 0 {
			continue
		}
		row := memberRecentPayment{Date: p.Date, Value: value, Outgoing: value < 0}
		if row.Outgoing {
			out -= value
			row.Note = p.PublicNote.String
			if p.ProjectID.Valid {
				row.Project = projectNames[p.ProjectID.Int64]
			}
		} else {
			in += value
			row.VS = p.Identification
		}
		rows = append(rows, row)
	}

	h.render(w, "member_payments_recent.html", map[string]interface{}{
		"Title":    "Poslední platby",
		"User":     user,
		"DBUser":   dbUser,
		"Access":   acc,
		"Payments": rows,
		"In":       in,
		"Out":      out,
	})
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
