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
