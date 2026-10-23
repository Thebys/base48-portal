package handler

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/base48/member-portal/internal/auth"
)

// A member record from the old system has no Keycloak id yet and is claimed
// by e-mail on first sign-in. Anyone can register in Keycloak with someone
// else's address, so the claim needs an address Keycloak verified (pentest
// F-13).
func TestClaimByEmailNeedsAVerifiedEmail(t *testing.T) {
	e := newAccessEnv(t)
	var legacyID int64
	if err := e.database.QueryRow(
		`INSERT INTO users (email, realname, phone, level_id, payments_id, state)
		 VALUES ('legacy@base48.cz', 'Old Member', '+420 777 000 000', 3, '751', 'accepted') RETURNING id`,
	).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	keycloakID := func() sql.NullString {
		t.Helper()
		var id sql.NullString
		if err := e.database.QueryRow(`SELECT keycloak_id FROM users WHERE id = ?`, legacyID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	impostor := &auth.User{ID: "kc-impostor", Email: "Legacy@base48.cz", EmailVerified: false, PreferredName: "impostor"}
	for _, path := range []string{"/profile", "/workshop", "/api/member/workshop"} {
		if rec := e.do(impostor, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("unverified GET %s = %d, want 403", path, rec.Code)
		}
	}
	if id := keycloakID(); id.Valid {
		t.Fatalf("the record was linked to %q on an unverified e-mail", id.String)
	}
	var logged int
	e.database.QueryRow(`SELECT COUNT(*) FROM system_logs WHERE subsystem = 'keycloak' AND level = 'warning' AND user_id = ?`, legacyID).Scan(&logged)
	if logged == 0 {
		t.Error("the refused claim was not logged")
	}

	owner := &auth.User{ID: "kc-owner", Email: "legacy@base48.cz", EmailVerified: true, PreferredName: "owner"}
	if rec := e.do(owner, http.MethodGet, "/profile", ""); rec.Code != http.StatusOK {
		t.Fatalf("verified GET /profile = %d", rec.Code)
	}
	if id := keycloakID(); id.String != "kc-owner" {
		t.Errorf("keycloak_id = %v, want kc-owner", id)
	}

	// A new address with no record behind it is no claim: it gets a new
	// applicant record, verified or not.
	newcomer := &auth.User{ID: "kc-new", Email: "new@base48.cz", EmailVerified: false, PreferredName: "new"}
	if rec := e.do(newcomer, http.MethodGet, "/profile", ""); rec.Code != http.StatusOK {
		t.Errorf("new unverified user GET /profile = %d, want 200", rec.Code)
	}
}
