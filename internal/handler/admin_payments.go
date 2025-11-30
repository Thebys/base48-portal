package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	if !user.IsAdmin() {
		http.Error(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

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

		// SKIP ALL OUTGOING PAYMENTS (negative or zero amounts)
		if amountFloat <= 0 {
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
		_, err = h.queries.GetProjectByPaymentsID(ctx, sql.NullString{String: payment.Identification, Valid: true})
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
	user := h.auth.GetUser(r)
	if user == nil {
		h.jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !user.IsAdmin() {
		h.jsonError(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

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
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	adminUsername := "unknown"
	if adminDBUser.Username.Valid {
		adminUsername = adminDBUser.Username.String
	}
	targetUsername := "unknown"
	if targetUser.Username.Valid {
		targetUsername = targetUser.Username.String
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message: fmt.Sprintf("Admin %s (%s) manually assigned payment #%d (%.2f Kč) to user %s (%s), VS set to '%s'",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			targetUsername, targetUser.Email,
			targetUser.PaymentsID.String),
		Metadata: sql.NullString{
			String: fmt.Sprintf(`{"admin_user_id":%d,"target_user_id":%d,"payment_id":%d,"amount":"%s","vs":"%s","staff_comment":"%s"}`,
				adminDBUser.ID, targetUser.ID, payment.ID, payment.Amount, targetUser.PaymentsID.String, req.StaffComment),
			Valid: true,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Payment successfully assigned and VS updated",
	})
}

// Helper function to parse float from string
func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
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
	user := h.auth.GetUser(r)
	if user == nil {
		h.jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !user.IsAdmin() {
		h.jsonError(w, "Forbidden - admin access required", http.StatusForbidden)
		return
	}

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
	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	adminUsername := "unknown"
	if adminDBUser.Username.Valid {
		adminUsername = adminDBUser.Username.String
	}

	// Build log message based on action type
	var logMessage string
	var metadata string

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
		metadata = fmt.Sprintf(`{"admin_user_id":%d,"action":"assign_user","payment_id":%d,"target_user_id":%d,"amount":"%s","vs":"%s","staff_comment":"%s"}`,
			adminDBUser.ID, payment.ID, targetUser.ID, payment.Amount, identification, req.StaffComment)

	case "project":
		logMessage = fmt.Sprintf("Admin %s (%s) updated payment #%d (%.2f Kč) and assigned to project '%s', VS set to '%s'",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			targetProject.Name,
			identification)
		metadata = fmt.Sprintf(`{"admin_user_id":%d,"action":"assign_project","payment_id":%d,"target_project_id":%d,"project_name":"%s","amount":"%s","vs":"%s","staff_comment":"%s"}`,
			adminDBUser.ID, payment.ID, targetProject.ID, targetProject.Name, payment.Amount, identification, req.StaffComment)

	default: // "unmatched" or no assignment
		logMessage = fmt.Sprintf("Admin %s (%s) updated payment #%d (%.2f Kč) data without assignment, VS set to '%s'",
			adminUsername, adminDBUser.Email,
			payment.ID, parseFloat(payment.Amount),
			identification)
		metadata = fmt.Sprintf(`{"admin_user_id":%d,"action":"update_unmatched","payment_id":%d,"amount":"%s","vs":"%s","message":"%s","staff_comment":"%s"}`,
			adminDBUser.ID, payment.ID, payment.Amount, identification, req.Message, req.StaffComment)
	}

	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "admin",
		Level:     "info",
		UserID:    sql.NullInt64{Int64: adminDBUser.ID, Valid: true},
		Message:   logMessage,
		Metadata: sql.NullString{
			String: metadata,
			Valid:  true,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Payment updated successfully",
	})
}
