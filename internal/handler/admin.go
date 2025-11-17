package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/base48/member-portal/internal/keycloak"
)

// allowedManagedRoles defines which roles can be managed via admin API (whitelist for security)
var allowedManagedRoles = map[string]bool{
	"active_member": true,
	"in_debt":       true,
}

// AdminRoleAssignRequest represents a role assignment/removal request
type AdminRoleAssignRequest struct {
	UserID   string `json:"user_id"`
	RoleName string `json:"role_name"`
}

// AdminRoleResponse represents the response for role operations
type AdminRoleResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// RequireAdmin middleware ensures user has memberportal_admin role
func (h *Handler) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := h.auth.GetUser(r)
		if user == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if !user.IsAdmin() {
			http.Error(w, "Forbidden - admin access required", http.StatusForbidden)
			return
		}

		next(w, r)
	}
}

// AdminAssignRoleHandler assigns a role to a user (admin only)
// POST /api/admin/roles/assign
// Body: {"user_id": "keycloak-user-id", "role_name": "active_member"}
func (h *Handler) AdminAssignRoleHandler(w http.ResponseWriter, r *http.Request) {
	var req AdminRoleAssignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.UserID == "" || req.RoleName == "" {
		h.jsonError(w, "user_id and role_name are required", http.StatusBadRequest)
		return
	}

	// Validate role name (whitelist for security)
	if !allowedManagedRoles[req.RoleName] {
		h.jsonError(w, fmt.Sprintf("Invalid role: %s. Allowed roles: active_member, in_debt", req.RoleName), http.StatusBadRequest)
		return
	}

	// Get service account token for Keycloak admin operations
	if h.serviceAccount == nil {
		h.jsonError(w, "Service account not configured", http.StatusInternalServerError)
		return
	}

	accessToken, err := h.serviceAccount.GetAccessToken(r.Context())
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Failed to get service account token: %v", err), http.StatusInternalServerError)
		return
	}

	// Create Keycloak client with service account token
	kcClient := keycloak.NewClient(h.config, accessToken)

	// Assign the role
	if err := kcClient.AssignRoleToUser(r.Context(), req.UserID, req.RoleName); err != nil {
		h.jsonError(w, fmt.Sprintf("Failed to assign role: %v", err), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, fmt.Sprintf("Role %s assigned to user %s", req.RoleName, req.UserID))
}

// AdminRemoveRoleHandler removes a role from a user (admin only)
// POST /api/admin/roles/remove
// Body: {"user_id": "keycloak-user-id", "role_name": "active_member"}
func (h *Handler) AdminRemoveRoleHandler(w http.ResponseWriter, r *http.Request) {
	var req AdminRoleAssignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.UserID == "" || req.RoleName == "" {
		h.jsonError(w, "user_id and role_name are required", http.StatusBadRequest)
		return
	}

	// Validate role name (whitelist for security)
	if !allowedManagedRoles[req.RoleName] {
		h.jsonError(w, fmt.Sprintf("Invalid role: %s. Allowed roles: active_member, in_debt", req.RoleName), http.StatusBadRequest)
		return
	}

	// Get service account token for Keycloak admin operations
	if h.serviceAccount == nil {
		h.jsonError(w, "Service account not configured", http.StatusInternalServerError)
		return
	}

	accessToken, err := h.serviceAccount.GetAccessToken(r.Context())
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Failed to get service account token: %v", err), http.StatusInternalServerError)
		return
	}

	// Create Keycloak client with service account token
	kcClient := keycloak.NewClient(h.config, accessToken)

	// Remove the role
	if err := kcClient.RemoveRoleFromUser(r.Context(), req.UserID, req.RoleName); err != nil {
		h.jsonError(w, fmt.Sprintf("Failed to remove role: %v", err), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, fmt.Sprintf("Role %s removed from user %s", req.RoleName, req.UserID))
}

// AdminGetUserRolesHandler gets all roles for a user (admin only)
// GET /api/admin/users/:userID/roles
func (h *Handler) AdminGetUserRolesHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		h.jsonError(w, "user_id query parameter is required", http.StatusBadRequest)
		return
	}

	// Get service account token for Keycloak admin operations
	if h.serviceAccount == nil {
		h.jsonError(w, "Service account not configured", http.StatusInternalServerError)
		return
	}

	accessToken, err := h.serviceAccount.GetAccessToken(r.Context())
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Failed to get service account token: %v", err), http.StatusInternalServerError)
		return
	}

	// Create Keycloak client with service account token
	kcClient := keycloak.NewClient(h.config, accessToken)

	// Get user roles
	roles, err := kcClient.GetUserRoles(r.Context(), userID)
	if err != nil {
		h.jsonError(w, fmt.Sprintf("Failed to get user roles: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"roles":   roles,
	})
}

// jsonError sends a JSON error response
func (h *Handler) jsonError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(AdminRoleResponse{
		Success: false,
		Error:   message,
	})
}

// jsonSuccess sends a JSON success response
func (h *Handler) jsonSuccess(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AdminRoleResponse{
		Success: true,
		Message: message,
	})
}
