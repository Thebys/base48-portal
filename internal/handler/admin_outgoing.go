package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/db"
)

// OutgoingPaymentView is one row of the admin's outgoing payments page.
type OutgoingPaymentView struct {
	db.ListOutgoingPaymentsRow
	Value       float64 // Amount as a number, negative
	AccountName string  // counter-account name, message and comment as
	Message     string  // the bank sent them, for the admin to go by
	Comment     string
}

// AdminOutgoingPaymentsHandler lists a year's outgoing payments for booking
// them to projects and writing what members see of them.
// GET /admin/payments/outgoing?year=2026&unassigned=1
func (h *Handler) AdminOutgoingPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin
	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()

	years, err := h.queries.ListOutgoingPaymentYears(ctx)
	if err != nil {
		log.Printf("[Outgoing] list years: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	year := r.URL.Query().Get("year")
	if year == "" {
		year = strconv.Itoa(time.Now().Year())
		if len(years) > 0 && years[0] < year {
			year = years[0]
		}
	}
	unassignedOnly := r.URL.Query().Get("unassigned") == "1"

	rows, err := h.queries.ListOutgoingPayments(ctx, year)
	if err != nil {
		log.Printf("[Outgoing] list %s: %v", year, err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	projects, err := h.queries.ListProjects(ctx)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	var payments []OutgoingPaymentView
	var total, assigned float64
	unassignedCount := 0
	for _, row := range rows {
		value, _ := strconv.ParseFloat(row.Amount, 64)
		total -= value
		if row.ProjectID.Valid {
			assigned -= value
		} else {
			unassignedCount++
		}
		if unassignedOnly && row.ProjectID.Valid {
			continue
		}
		view := OutgoingPaymentView{ListOutgoingPaymentsRow: row, Value: value}
		view.AccountName, view.Message, view.Comment = bankTexts(row.RawData)
		payments = append(payments, view)
	}

	h.render(w, "admin_payments_outgoing.html", map[string]interface{}{
		"Title":           "Odchozí platby",
		"User":            user,
		"DBUser":          dbUser,
		"Year":            year,
		"Years":           years,
		"UnassignedOnly":  unassignedOnly,
		"Payments":        payments,
		"Projects":        projects,
		"Total":           total,
		"Assigned":        assigned,
		"Count":           len(rows),
		"UnassignedCount": unassignedCount,
	})
}

// bankTexts digs the counter-account name, the message for the recipient
// and the payer's comment out of raw_data. It comes in two shapes: FIO's own,
// {"column10": {"value": "..."}}, from the old portal's import, and
// {"column10": "..."} from this portal's sync.
func bankTexts(raw sql.NullString) (accountName, message, comment string) {
	var columns map[string]json.RawMessage
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &columns) != nil {
		return "", "", ""
	}
	text := func(key string) string {
		var s string
		if json.Unmarshal(columns[key], &s) == nil {
			return strings.TrimSpace(s)
		}
		var wrapped struct{ Value *string }
		if json.Unmarshal(columns[key], &wrapped) == nil && wrapped.Value != nil {
			return strings.TrimSpace(*wrapped.Value)
		}
		return ""
	}
	return text("column10"), text("column16"), text("column25")
}

// AssignOutgoingRequest books an outgoing payment to a project (0 for none)
// and sets the note members see of it.
type AssignOutgoingRequest struct {
	PaymentID  int64  `json:"payment_id"`
	ProjectID  int64  `json:"project_id"`
	PublicNote string `json:"public_note"`
}

// maxPublicNote is how long a note members see may be, in characters.
const maxPublicNote = 200

// AdminAssignOutgoingHandler saves the project and note of an outgoing payment.
// POST /api/admin/payments/outgoing
func (h *Handler) AdminAssignOutgoingHandler(w http.ResponseWriter, r *http.Request) {
	var req AssignOutgoingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.PublicNote = strings.TrimSpace(req.PublicNote)
	if len([]rune(req.PublicNote)) > maxPublicNote {
		h.jsonError(w, fmt.Sprintf("Popis může mít nejvýš %d znaků", maxPublicNote), http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	project := sql.NullInt64{}
	projectName := ""
	if req.ProjectID != 0 {
		p, err := h.queries.GetProject(ctx, req.ProjectID)
		if err != nil {
			h.jsonError(w, "Project not found", http.StatusNotFound)
			return
		}
		project = sql.NullInt64{Int64: p.ID, Valid: true}
		projectName = p.Name
	}

	before, err := h.queries.GetPayment(ctx, req.PaymentID)
	if err != nil {
		h.jsonError(w, "Payment not found", http.StatusNotFound)
		return
	}
	payment, err := h.queries.AssignOutgoingPayment(ctx, db.AssignOutgoingPaymentParams{
		ID:         req.PaymentID,
		ProjectID:  project,
		PublicNote: sql.NullString{String: req.PublicNote, Valid: req.PublicNote != ""},
	})
	if err == sql.ErrNoRows {
		h.jsonError(w, "Only outgoing payments are assigned here", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("[Outgoing] assign payment %d: %v", req.PaymentID, err)
		h.jsonError(w, "Failed to save", http.StatusInternalServerError)
		return
	}

	// best-effort audit log
	admin, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: h.auth.GetUser(r).ID, Valid: true})
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: admin.ID, Valid: admin.ID != 0},
		Message: fmt.Sprintf("Admin %s set outgoing payment #%d (%s Kč) to project %q, note %q",
			admin.Email, payment.ID, payment.Amount, projectName, req.PublicNote),
		Metadata: logMetadata(map[string]interface{}{
			"payment_id":       payment.ID,
			"project_id":       req.ProjectID,
			"previous_project": before.ProjectID.Int64,
			"public_note":      req.PublicNote,
			"previous_note":    before.PublicNote.String,
		}),
	})

	h.jsonSuccess(w, "Uloženo")
}
