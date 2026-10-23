package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/base48/member-portal/internal/config"
)

// fakeRealm records every request and answers role lookups, so a test can see
// exactly which calls reached "Keycloak".
func fakeRealm(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(Role{ID: "role-1", Name: "active_member"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func TestReadOnlyRefusesRoleChanges(t *testing.T) {
	srv, calls := fakeRealm(t)
	c := NewClient(&config.Config{KeycloakURL: srv.URL, KeycloakRealm: "hackerspace", KeycloakReadOnly: true}, "token")
	ctx := context.Background()

	if err := c.AssignRoleToUser(ctx, "user-1", "active_member"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("AssignRoleToUser = %v, want ErrReadOnly", err)
	}
	if err := c.RemoveRoleFromUser(ctx, "user-1", "active_member"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("RemoveRoleFromUser = %v, want ErrReadOnly", err)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("read-only client still called Keycloak: %v", got)
	}

	// Reading is what read-only is for.
	if _, err := c.GetRoleByName(ctx, "active_member"); err != nil {
		t.Errorf("GetRoleByName in read-only mode: %v", err)
	}
}

func TestWritableClientChangesRoles(t *testing.T) {
	srv, calls := fakeRealm(t)
	c := NewClient(&config.Config{KeycloakURL: srv.URL, KeycloakRealm: "hackerspace"}, "token")
	ctx := context.Background()

	if err := c.AssignRoleToUser(ctx, "user-1", "active_member"); err != nil {
		t.Fatalf("AssignRoleToUser: %v", err)
	}
	if err := c.RemoveRoleFromUser(ctx, "user-1", "active_member"); err != nil {
		t.Fatalf("RemoveRoleFromUser: %v", err)
	}
	want := []string{
		"GET /admin/realms/hackerspace/roles/active_member",
		"POST /admin/realms/hackerspace/users/user-1/role-mappings/realm",
		"GET /admin/realms/hackerspace/roles/active_member",
		"DELETE /admin/realms/hackerspace/users/user-1/role-mappings/realm",
	}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// user_id from /api/admin/users/roles went into the Admin API path as is, so
// "../clients/<id>/client-secret?x=" sent the service account's token to a
// different endpoint (pentest F-15).
func TestUserIDCannotLeaveTheUserPath(t *testing.T) {
	srv, calls := fakeRealm(t)
	c := NewClient(&config.Config{KeycloakURL: srv.URL, KeycloakRealm: "hackerspace"}, "token")
	ctx := context.Background()

	for _, id := range []string{
		"../clients/0b1e/client-secret?x=",
		"../roles#",
		"..",
		".",
		"a/../../b",
		"%2e%2e%2fclients",
		"id?first=0",
		"id#x",
		"",
		"with space",
	} {
		if _, err := c.GetUserRoles(ctx, id); !errors.Is(err, ErrBadUserID) {
			t.Errorf("GetUserRoles(%q) = %v, want ErrBadUserID", id, err)
		}
		if err := c.AssignRoleToUser(ctx, id, "active_member"); !errors.Is(err, ErrBadUserID) {
			t.Errorf("AssignRoleToUser(%q) = %v, want ErrBadUserID", id, err)
		}
		if err := c.RemoveRoleFromUser(ctx, id, "active_member"); !errors.Is(err, ErrBadUserID) {
			t.Errorf("RemoveRoleFromUser(%q) = %v, want ErrBadUserID", id, err)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("bad user ids reached Keycloak: %v", got)
	}

	// Real ids: a local user's UUID and a federated user's.
	for _, id := range []string{"6f1c2b0e-9a4d-4c3e-8b7a-1d2e3f405162", "f:3a4b5c6d-7e8f-4012-9345-6789abcdef01:jan.novak"} {
		if err := c.AssignRoleToUser(ctx, id, "active_member"); err != nil {
			t.Errorf("AssignRoleToUser(%q): %v", id, err)
		}
	}
	want := []string{
		"GET /admin/realms/hackerspace/roles/active_member",
		"POST /admin/realms/hackerspace/users/6f1c2b0e-9a4d-4c3e-8b7a-1d2e3f405162/role-mappings/realm",
		"GET /admin/realms/hackerspace/roles/active_member",
		"POST /admin/realms/hackerspace/users/f:3a4b5c6d-7e8f-4012-9345-6789abcdef01:jan.novak/role-mappings/realm",
	}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}
