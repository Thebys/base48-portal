package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/base48/member-portal/internal/access"
	"github.com/base48/member-portal/internal/db"
)

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

// statementRow is one line of the admins' bank statement.
type statementRow struct {
	db.Payment
	Value       float64
	UserName    string
	ProjectName string
	RawPretty   string // raw_data indented for reading
}

// AdminRecentPaymentsHandler shows the bank statement of this and last month
// with everything the portal knows about each payment.
// GET /admin/payments/recent
func (h *Handler) AdminRecentPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // RequireAdmin let them in
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()

	payments, err := h.queries.ListRecentPayments(ctx)
	if err != nil {
		http.Error(w, "Failed to load payments", http.StatusInternalServerError)
		return
	}
	users, err := h.queries.ListUsers(ctx)
	if err != nil {
		http.Error(w, "Failed to load payments", http.StatusInternalServerError)
		return
	}
	projects, err := h.queries.ListProjects(ctx)
	if err != nil {
		http.Error(w, "Failed to load payments", http.StatusInternalServerError)
		return
	}
	userNames := make(map[int64]string, len(users))
	for _, u := range users {
		userNames[u.ID] = u.DisplayName()
	}
	projectNames := make(map[int64]string, len(projects))
	for _, p := range projects {
		projectNames[p.ID] = p.Name
	}

	rows := make([]statementRow, 0, len(payments))
	for _, p := range payments {
		row := statementRow{Payment: p}
		row.Value, _ = strconv.ParseFloat(p.Amount, 64)
		if p.UserID.Valid {
			row.UserName = userNames[p.UserID.Int64]
		}
		if p.ProjectID.Valid {
			row.ProjectName = projectNames[p.ProjectID.Int64]
		}
		var pretty bytes.Buffer
		if p.RawData.Valid && json.Indent(&pretty, []byte(p.RawData.String), "", "  ") == nil {
			row.RawPretty = pretty.String()
		} else {
			row.RawPretty = p.RawData.String
		}
		rows = append(rows, row)
	}

	h.render(w, "admin_payments_recent.html", map[string]interface{}{
		"Title":    "Výpis plateb",
		"User":     user,
		"DBUser":   dbUser,
		"Payments": rows,
	})
}
