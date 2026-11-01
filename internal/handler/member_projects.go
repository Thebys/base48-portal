package handler

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/base48/member-portal/internal/access"
)

// memberProject is a project as the fundraising page shows it.
type memberProject struct {
	projectSummary
	PrimaryVS  string
	SpentShare int // how much of what was raised is spent, 0-100, for the bar
	Payments   []memberProjectPayment
}

// memberProjectPayment is a payment of a project as members see it. An
// expense shows only its date, amount and note; who was paid, and the bank's
// own texts, stay with the admins.
type memberProjectPayment struct {
	Date          time.Time
	Value         float64
	Outgoing      bool
	Note          string // expenses: the note an admin approved
	RemoteAccount string // gifts: the donor's account, for members only
	VS            string // gifts
}

// MemberProjectsHandler shows the fundraising projects: what each raised and
// spent, and the payments behind both.
// GET /projects
func (h *Handler) MemberProjectsHandler(w http.ResponseWriter, r *http.Request) {
	user, dbUser, acc, ok := h.memberPage(w, r, access.Subject.Fundraising)
	if !ok {
		return
	}
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

	byProject := make(map[int64][]memberProjectPayment)
	for _, p := range payments {
		value, _ := strconv.ParseFloat(p.Amount, 64)
		view := memberProjectPayment{Date: p.Date, Value: value, Outgoing: value < 0}
		if view.Outgoing {
			view.Note = p.PublicNote.String
		} else {
			view.VS = p.Identification
			// Donors' account numbers are for members only.
			if acc.MemberArea() {
				view.RemoteAccount = p.RemoteAccount
			}
		}
		byProject[p.ProjectID.Int64] = append(byProject[p.ProjectID.Int64], view)
	}

	projects := make([]memberProject, len(summaries))
	for i, s := range summaries {
		projects[i] = memberProject{projectSummary: s, Payments: byProject[s.ID]}
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

	h.render(w, "member_projects.html", map[string]interface{}{
		"Title":    "Fundraising",
		"User":     user,
		"DBUser":   dbUser,
		"Access":   acc,
		"Projects": projects,
	})
}
