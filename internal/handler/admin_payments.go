package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/unmatched"
)

// UnmatchedPaymentInfo is one row of the unmatched payments page.
type UnmatchedPaymentInfo struct {
	Payment  db.Payment
	Category string // an unmatched.Category
	Reason   string
}

// AdminUnmatchedPaymentsHandler shows all payments that couldn't be automatically matched to users
// GET /admin/payments/unmatched
func (h *Handler) AdminUnmatchedPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware
	ctx := r.Context()

	// Get database user
	dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	payments, err := unmatched.List(ctx, h.queries)
	if err != nil {
		log.Printf("[Payments] list unmatched: %v", err)
		http.Error(w, "Failed to fetch unassigned payments", http.StatusInternalServerError)
		return
	}
	unmatchedList := make([]UnmatchedPaymentInfo, len(payments))
	counts := map[unmatched.Category]int{}
	for i, p := range payments {
		unmatchedList[i] = UnmatchedPaymentInfo{
			Payment:  p.Payment,
			Category: string(p.Category),
			Reason:   p.Category.Reason(),
		}
		counts[p.Category]++
	}

	// Get dismissed payments for archive section
	allDismissedPayments, err := h.queries.ListDismissedPayments(ctx)
	if err != nil {
		http.Error(w, "Failed to fetch dismissed payments", http.StatusInternalServerError)
		return
	}

	// Dismissed payments that belong to a project are the project's, not the archive's.
	var dismissedPayments []db.Payment
	dismissedTotal := 0.0
	for _, p := range allDismissedPayments {
		// Skip small amounts
		amountFloat := 0.0
		if amount, err := strconv.ParseFloat(p.Amount, 64); err == nil {
			amountFloat = amount
		}
		if amountFloat < unmatched.MinAmount {
			continue
		}

		if p.ProjectID.Valid {
			continue
		}

		dismissedPayments = append(dismissedPayments, p)
		dismissedTotal += amountFloat
	}

	// Prepare template data
	data := map[string]interface{}{
		"User":              user,
		"DBUser":            dbUser,
		"UnmatchedList":     unmatchedList,
		"TotalCount":        len(unmatchedList),
		"CountEmptyVS":      counts[unmatched.EmptyVS],
		"CountUserNotFound": counts[unmatched.UserNotFound],
		"CountSyncBug":      counts[unmatched.SyncBug],
		"DismissedPayments": dismissedPayments,
		"DismissedCount":    len(dismissedPayments),
		"DismissedTotal":    dismissedTotal,
	}

	h.render(w, "admin_payments_unmatched.html", data)
}

// AssignPaymentRequest is the request body for assigning a payment
type AssignPaymentRequest struct {
	PaymentID    int64  `json:"payment_id"`
	UserID       int64  `json:"user_id"`
	StaffComment string `json:"staff_comment"`
}

// AdminAssignPaymentHandler manually assigns an unmatched payment to a user
// POST /api/admin/payments/assign
// IMPORTANT: This also sets the payment's identification to the user's payments_id
// so that it will be counted in the user's balance calculation
func (h *Handler) AdminAssignPaymentHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	var req AssignPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Verify payment exists
	payment, err := h.queries.GetPayment(ctx, req.PaymentID)
	if err != nil {
		h.jsonError(w, "Payment not found", http.StatusNotFound)
		return
	}

	// Verify user exists
	targetUser, err := h.queries.GetUserByID(ctx, req.UserID)
	if err != nil {
		h.jsonError(w, "User not found", http.StatusNotFound)
		return
	}

	// CRITICAL: We need to update BOTH user_id AND identification
	// The identification must match user.payments_id for the payment to count in balance
	staffComment := sql.NullString{}
	if req.StaffComment != "" {
		staffComment = sql.NullString{String: req.StaffComment, Valid: true}
	}

	// Use UpsertPayment to update all fields including identification
	_, err = h.queries.UpsertPayment(ctx, db.UpsertPaymentParams{
		UserID:         sql.NullInt64{Int64: req.UserID, Valid: true},
		ProjectID:      sql.NullInt64{}, // Clear project assignment when assigning to user
		Date:           payment.Date,
		Amount:         payment.Amount,
		Kind:           payment.Kind,
		KindID:         payment.KindID,
		LocalAccount:   payment.LocalAccount,
		RemoteAccount:  payment.RemoteAccount,
		Identification: targetUser.PaymentsID.String, // SET VS to user's payments_id!
		RawData:        payment.RawData,
		StaffComment:   staffComment,
	})

	if err != nil {
		h.jsonError(w, "Failed to assign payment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log the assignment
	adminDBUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		log.Printf("[Handler] failed to fetch admin DB user: %v", err)
	}

	adminUsername := "unknown"
	if adminDBUser.Username.Valid {
		adminUsername = adminDBUser.Username.String
	}
	targetUsername := "unknown"
	if targetUser.Username.Valid {
		targetUsername = targetUser.Username.String
	}

	// best-effort audit log
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message: fmt.Sprintf("Admin %s (%s) manually assigned payment #%d (%.2f Kč) to user %s (%s), VS set to '%s'",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			targetUsername, targetUser.Email,
			targetUser.PaymentsID.String),
		Metadata: logMetadata(map[string]interface{}{
			"admin_user_id":  adminDBUser.ID,
			"target_user_id": targetUser.ID,
			"payment_id":     payment.ID,
			"amount":         payment.Amount,
			"vs":             targetUser.PaymentsID.String,
			"staff_comment":  req.StaffComment,
		}),
	})

	h.jsonSuccess(w, "Payment successfully assigned and VS updated")
}

// Helper function to parse float from string
func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

// DismissPaymentRequest is the request body for dismissing a payment
type DismissPaymentRequest struct {
	PaymentID int64  `json:"payment_id"`
	Reason    string `json:"reason"`
}

// AdminDismissPaymentHandler marks a payment as dismissed (seen/ignored)
// POST /api/admin/payments/dismiss
func (h *Handler) AdminDismissPaymentHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	var req DismissPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Get admin user from DB
	adminDBUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Verify payment exists
	payment, err := h.queries.GetPayment(ctx, req.PaymentID)
	if err != nil {
		h.jsonError(w, "Payment not found", http.StatusNotFound)
		return
	}

	// Dismiss the payment
	staffComment := sql.NullString{}
	if req.Reason != "" {
		staffComment = sql.NullString{String: "[DISMISSED] " + req.Reason, Valid: true}
	} else {
		staffComment = sql.NullString{String: "[DISMISSED]", Valid: true}
	}

	_, err = h.queries.DismissPayment(ctx, db.DismissPaymentParams{
		DismissedBy:     adminDBUser.ID,
		DismissedReason: req.Reason,
		StaffComment:    staffComment,
		ID:              req.PaymentID,
	})

	if err != nil {
		h.jsonError(w, "Failed to dismiss payment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log the action
	adminUsername := "unknown"
	if adminDBUser.Username.Valid {
		adminUsername = adminDBUser.Username.String
	}

	// best-effort audit log
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message: fmt.Sprintf("Admin %s (%s) dismissed payment #%d (%.2f Kč) - reason: %s",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			req.Reason),
		Metadata: logMetadata(map[string]interface{}{
			"admin_user_id": adminDBUser.ID,
			"payment_id":    payment.ID,
			"amount":        payment.Amount,
			"reason":        req.Reason,
		}),
	})

	h.jsonSuccess(w, "Payment dismissed successfully")
}

// UndismissPaymentRequest is the request body for undismissing a payment
type UndismissPaymentRequest struct {
	PaymentID int64 `json:"payment_id"`
}

// AdminUndismissPaymentHandler restores a dismissed payment back to unmatched
// POST /api/admin/payments/undismiss
func (h *Handler) AdminUndismissPaymentHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	var req UndismissPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Get admin user from DB
	adminDBUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Verify payment exists
	payment, err := h.queries.GetPayment(ctx, req.PaymentID)
	if err != nil {
		h.jsonError(w, "Payment not found", http.StatusNotFound)
		return
	}

	// Undismiss the payment
	_, err = h.queries.UndismissPayment(ctx, req.PaymentID)
	if err != nil {
		h.jsonError(w, "Failed to undismiss payment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log the action
	adminUsername := "unknown"
	if adminDBUser.Username.Valid {
		adminUsername = adminDBUser.Username.String
	}

	// best-effort audit log
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message: fmt.Sprintf("Admin %s (%s) restored payment #%d (%.2f Kč) from archive",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount)),
		Metadata: logMetadata(map[string]interface{}{
			"admin_user_id": adminDBUser.ID,
			"payment_id":    payment.ID,
			"amount":        payment.Amount,
		}),
	})

	h.jsonSuccess(w, "Payment restored successfully")
}

// UpdatePaymentRequest is the request body for updating a payment
type UpdatePaymentRequest struct {
	PaymentID    int64  `json:"payment_id"`
	VS           string `json:"vs"`
	StaffComment string `json:"staff_comment"`
	AssignType   string `json:"assign_type"` // "user", "project", "unmatched"
	UserID       *int64 `json:"user_id"`
	ProjectID    *int64 `json:"project_id"`
}

// AdminUpdatePaymentHandler updates payment data and optionally assigns it
// POST /api/admin/payments/update
func (h *Handler) AdminUpdatePaymentHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	var req UpdatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Verify payment exists
	payment, err := h.queries.GetPayment(ctx, req.PaymentID)
	if err != nil {
		h.jsonError(w, "Payment not found", http.StatusNotFound)
		return
	}

	// Determine assignment
	var userID sql.NullInt64
	var projectID sql.NullInt64
	var targetUser db.User
	var targetProject db.Project
	identification := req.VS

	switch req.AssignType {
	case "user":
		if req.UserID == nil {
			h.jsonError(w, "user_id required", http.StatusBadRequest)
			return
		}
		targetUser, err = h.queries.GetUserByID(ctx, *req.UserID)
		if err != nil {
			h.jsonError(w, "User not found", http.StatusNotFound)
			return
		}
		userID = sql.NullInt64{Int64: *req.UserID, Valid: true}
		identification = targetUser.PaymentsID.String

	case "project":
		if req.ProjectID == nil {
			h.jsonError(w, "project_id required", http.StatusBadRequest)
			return
		}
		targetProject, err = h.queries.GetProject(ctx, *req.ProjectID)
		if err != nil {
			h.jsonError(w, "Project not found", http.StatusNotFound)
			return
		}
		projectID = sql.NullInt64{Int64: *req.ProjectID, Valid: true}
		if targetProject.PaymentsID.Valid {
			identification = targetProject.PaymentsID.String
		}
	}

	staffComment := sql.NullString{}
	if req.StaffComment != "" {
		staffComment = sql.NullString{String: req.StaffComment, Valid: true}
	}

	_, err = h.queries.UpsertPayment(ctx, db.UpsertPaymentParams{
		UserID:         userID,
		ProjectID:      projectID,
		Date:           payment.Date,
		Amount:         payment.Amount,
		Kind:           payment.Kind,
		KindID:         payment.KindID,
		LocalAccount:   payment.LocalAccount,
		RemoteAccount:  payment.RemoteAccount,
		Identification: identification,
		RawData:        payment.RawData,
		StaffComment:   staffComment,
	})

	if err != nil {
		h.jsonError(w, "Failed to update: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Log the update action
	adminDBUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		log.Printf("[Handler] failed to fetch admin DB user: %v", err)
	}

	adminUsername := "unknown"
	if adminDBUser.Username.Valid {
		adminUsername = adminDBUser.Username.String
	}

	// Build log message and metadata based on action type
	var logMessage string
	var md map[string]interface{}

	switch req.AssignType {
	case "user":
		targetUsername := "unknown"
		if targetUser.Username.Valid {
			targetUsername = targetUser.Username.String
		}
		logMessage = fmt.Sprintf("Admin %s (%s) updated payment #%d (%.2f Kč) and assigned to user %s (%s), VS set to '%s'",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			targetUsername, targetUser.Email,
			identification)
		md = map[string]interface{}{
			"admin_user_id":  adminDBUser.ID,
			"action":         "assign_user",
			"payment_id":     payment.ID,
			"target_user_id": targetUser.ID,
			"amount":         payment.Amount,
			"vs":             identification,
			"staff_comment":  req.StaffComment,
		}

	case "project":
		logMessage = fmt.Sprintf("Admin %s (%s) updated payment #%d (%.2f Kč) and assigned to project '%s', VS set to '%s'",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			targetProject.Name,
			identification)
		md = map[string]interface{}{
			"admin_user_id":     adminDBUser.ID,
			"action":            "assign_project",
			"payment_id":        payment.ID,
			"target_project_id": targetProject.ID,
			"project_name":      targetProject.Name,
			"amount":            payment.Amount,
			"vs":                identification,
			"staff_comment":     req.StaffComment,
		}

	default: // "unmatched" or no assignment
		logMessage = fmt.Sprintf("Admin %s (%s) updated payment #%d (%.2f Kč) data without assignment, VS set to '%s'",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			identification)
		md = map[string]interface{}{
			"admin_user_id": adminDBUser.ID,
			"action":        "update_unmatched",
			"payment_id":    payment.ID,
			"amount":        payment.Amount,
			"vs":            identification,
			"staff_comment": req.StaffComment,
		}
	}

	// best-effort audit log
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message:   logMessage,
		Metadata:  logMetadata(md),
	})

	h.jsonSuccess(w, "Payment updated successfully")
}
