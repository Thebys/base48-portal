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
	PrimaryVS  string
	SpentShare int // how much of what was raised is spent, 0-100, for the bar
	Payments   []projectPaymentView
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

	projects := make([]projectView, len(summaries))
	for i, s := range summaries {
		projects[i] = projectView{projectSummary: s, Payments: byProject[s.ID]}
		if len(s.VSList) > 0 {
			projects[i].PrimaryVS = s.VSList[0].VS
		}
		switch {
		case s.Raised > 0:
			projects[i].SpentShare = int(min(100, s.Spent/s.Raised*100))
		case s.Spent > 0:
			projects[i].SpentShare = 100
		}
	}

	title := "Fundraising"
	if manage {
		title = "Správa projektů"
	}
	h.render(w, "projects.html", map[string]interface{}{
		"Title":    title,
		"User":     user,
		"DBUser":   dbUser,
		"Access":   acc,
		"Manage":   manage,
		"Projects": projects,
	})
}
