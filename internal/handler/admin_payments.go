package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/base48/member-portal/internal/db"
)

// UnmatchedPaymentInfo contains payment with analysis
type UnmatchedPaymentInfo struct {
	Payment     db.Payment
	UserExists  bool
	Category    string // "empty_vs", "user_not_found", "sync_bug"
	Reason      string
	IsIncoming  bool
	AmountFloat float64
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

	// Get all unassigned payments
	unassignedPayments, err := h.queries.ListUnassignedPayments(ctx)
	if err != nil {
		http.Error(w, "Failed to fetch unassigned payments", http.StatusInternalServerError)
		return
	}

	// Analyze each payment - ONLY INCOMING PAYMENTS
	var unmatchedList []UnmatchedPaymentInfo
	totalAmount := 0.0
	countPayments := 0

	// Count by category
	countEmptyVS := 0
	countUserNotFound := 0
	countSyncBug := 0

	for _, payment := range unassignedPayments {
		// Parse amount for totals and direction
		amountFloat := 0.0
		if amount, err := strconv.ParseFloat(payment.Amount, 64); err == nil {
			amountFloat = amount
		}

		// SKIP: outgoing payments (negative/zero) and small incoming (< 5 Kč, usually bank interest)
		if amountFloat < 5 {
			continue
		}

		// Only process incoming payments from here
		totalAmount += amountFloat
		countPayments++

		info := UnmatchedPaymentInfo{
			Payment:     payment,
			AmountFloat: amountFloat,
			IsIncoming:  true, // Always true now
		}

		// Skip if no identification (VS)
		if payment.Identification == "" {
			info.Category = "empty_vs"
			info.Reason = "Empty variable symbol"
			unmatchedList = append(unmatchedList, info)
			countEmptyVS++
			continue
		}

		// Check if this VS belongs to a project (skip if it does)
		_, err = h.queries.GetProjectByPaymentsID(ctx, payment.Identification)
		if err == nil {
			// Project exists with this VS - this is a project payment, skip it
			continue
		} else if err != sql.ErrNoRows {
			// Database error (not just "not found")
			http.Error(w, "Database error checking project", http.StatusInternalServerError)
			return
		}

		// Check if user with this payments_id exists
		_, err = h.queries.GetUserByPaymentsID(ctx, sql.NullString{String: payment.Identification, Valid: true})
		if err == sql.ErrNoRows {
			info.Category = "user_not_found"
			info.UserExists = false
			info.Reason = "No user with this payments_id exists"
			unmatchedList = append(unmatchedList, info)
			countUserNotFound++
			continue
		} else if err != nil {
			http.Error(w, "Database error checking user", http.StatusInternalServerError)
			return
		}

		// User exists but payment not assigned - potential sync bug
		info.Category = "sync_bug"
		info.UserExists = true
		info.Reason = "User with this payments_id exists but payment not assigned (sync issue)"
		unmatchedList = append(unmatchedList, info)
		countSyncBug++
	}

	// Get dismissed payments for archive section
	allDismissedPayments, err := h.queries.ListDismissedPayments(ctx)
	if err != nil {
		http.Error(w, "Failed to fetch dismissed payments", http.StatusInternalServerError)
		return
	}

	// Filter dismissed payments - exclude those with project VS (they belong to projects, not archive)
	var dismissedPayments []db.Payment
	dismissedTotal := 0.0
	for _, p := range allDismissedPayments {
		// Skip small amounts
		amountFloat := 0.0
		if amount, err := strconv.ParseFloat(p.Amount, 64); err == nil {
			amountFloat = amount
		}
		if amountFloat < 5 {
			continue
		}

		// Skip if VS belongs to a project
		if p.Identification != "" {
			_, err := h.queries.GetProjectByPaymentsID(ctx, p.Identification)
			if err == nil {
				// Project exists with this VS - skip, it's a project payment
				continue
			}
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
		"TotalAmount":       totalAmount,
		"CountPayments":     countPayments,
		"CountEmptyVS":      countEmptyVS,
		"CountUserNotFound": countUserNotFound,
		"CountSyncBug":      countSyncBug,
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
	PaymentID    int64   `json:"payment_id"`
	VS           string  `json:"vs"`
	Message      string  `json:"message"`
	Comment      string  `json:"comment"`
	StaffComment string  `json:"staff_comment"`
	AssignType   string  `json:"assign_type"` // "user", "project", "unmatched"
	UserID       *int64  `json:"user_id"`
	ProjectID    *int64  `json:"project_id"`
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
			"message":       req.Message,
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
