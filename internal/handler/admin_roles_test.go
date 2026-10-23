package handler

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The role endpoints answer 400 for a user id that is not one, before they
// look for a service account token (pentest F-15).
func TestAdminRoleEndpointsRefuseBadUserIDs(t *testing.T) {
	e := newAccessEnv(t)
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	r := e.router.(*chi.Mux)
	r.Get("/api/admin/users/roles", e.h.RequireAdmin(e.h.AdminGetUserRolesHandler))
	r.Post("/api/admin/roles/assign", e.h.RequireAdmin(e.h.AdminAssignRoleHandler))
	r.Post("/api/admin/roles/remove", e.h.RequireAdmin(e.h.AdminRemoveRoleHandler))

	bad := "../clients/0b1e/client-secret?x="
	if rec := e.do(admin, http.MethodGet, "/api/admin/users/roles?user_id="+url.QueryEscape(bad), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("GET roles of %q = %d, want 400", bad, rec.Code)
	}
	for _, path := range []string{"/api/admin/roles/assign", "/api/admin/roles/remove"} {
		body := `{"user_id":"../groups/1","role_name":"active_member"}`
		if rec := e.do(admin, http.MethodPost, path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s with a traversing user_id = %d, want 400", path, rec.Code)
		}
	}
}
