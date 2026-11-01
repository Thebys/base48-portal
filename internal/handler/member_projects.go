package handler

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/base48/member-portal/internal/access"
	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/db"
)

// projectView is a project as the fundraising page shows it.
type projectView struct {
	projectSummary
	PrimaryVS string
	Bar       projectBar
	Payments  []projectPaymentView
	Manage    bool // the admin's controls
}

// projectBar splits the larger of raised and spent into what was spent from
// the money raised, what is left of it, and what the association covered
// beyond it, each as a share of the bar in percent. The page draws it once
// something is spent: a bar of nothing but money left reads as a goal met.
type projectBar struct {
	Spent, Left, Covered float64
}

func newProjectBar(raised, spent float64) projectBar {
	whole := max(raised, spent)
	if whole <= 0 {
		return projectBar{}
	}
	return projectBar{
		Spent:   min(spent, raised) / whole * 100,
		Left:    max(raised-spent, 0) / whole * 100,
		Covered: max(spent-raised, 0) / whole * 100,
	}
}

// projectPaymentView is a payment of a project. Members see date, amount and
// the note of an expense; whose account a payment came from or went to stays
// with the admins, as does the staff comment. Private by default: a gift
// says nothing about the giver.
type projectPaymentView struct {
	ID            int64
	Date          time.Time
	Value         float64
	Outgoing      bool
	Note          string // expenses: the note an admin approved
	RemoteAccount string
	VS            string // gifts
	StaffComment  string // admins only
}

// MemberProjectsHandler shows the fundraising projects: what each raised and
// spent, and the payments behind both.
// GET /projects
func (h *Handler) MemberProjectsHandler(w http.ResponseWriter, r *http.Request) {
	user, dbUser, acc, ok := h.memberPage(w, r, access.Subject.Fundraising)
	if !ok {
		return
	}
	h.renderProjects(w, r, user, dbUser, acc, false)
}

// AdminProjectsHandler shows the same page with the controls to manage
// projects, and what members do not see of their payments.
// GET /admin/projects
func (h *Handler) AdminProjectsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // RequireAdmin let them in
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	h.renderProjects(w, r, user, dbUser, h.accessFor(r.Context(), user, dbUser), true)
}

// renderProjects renders projects.html for a member, or with manage for an
// admin.
func (h *Handler) renderProjects(w http.ResponseWriter, r *http.Request, user *auth.User, dbUser *db.User, acc access.Subject, manage bool) {
	ctx := r.Context()
	summaries, err := h.listProjectSummaries(ctx)
	if err != nil {
		log.Printf("[Projects] list: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	payments, err := h.queries.ListAssignedProjectPayments(ctx)
	if err != nil {
		log.Printf("[Projects] list payments: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	byProject := make(map[int64][]projectPaymentView)
	for _, p := range payments {
		value, _ := strconv.ParseFloat(p.Amount, 64)
		view := projectPaymentView{ID: p.ID, Date: p.Date, Value: value, Outgoing: value < 0}
		if view.Outgoing {
			view.Note = p.PublicNote.String
		} else {
			view.VS = p.Identification
		}
		if manage {
			view.RemoteAccount = p.RemoteAccount
			view.StaffComment = p.StaffComment.String
		}
		byProject[p.ProjectID.Int64] = append(byProject[p.ProjectID.Int64], view)
	}

	var open, closed []projectView
	for _, s := range summaries {
		view := projectView{
			projectSummary: s,
			Bar:            newProjectBar(s.Raised, s.Spent),
			Payments:       byProject[s.ID],
			Manage:         manage,
		}
		if len(s.VSList) > 0 {
			view.PrimaryVS = s.VSList[0].VS
		}
		if s.ArchivedAt.Valid {
			closed = append(closed, view)
		} else {
			open = append(open, view)
		}
	}

	title := "Fundraising"
	if manage {
		title = "Správa projektů"
	}
	h.render(w, "projects.html", map[string]interface{}{
		"Title":  title,
		"User":   user,
		"DBUser": dbUser,
		"Access": acc,
		"Manage": manage,
		"Open":   open,
		"Closed": closed,
	})
}
