package handler

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The pentest of 2026-10-23 set a member's own fee to "NaN": it passed the
// "< minimum" check, was stored, and SQLite summed it as 0.
func TestCustomFeeMustBeAPlainAmount(t *testing.T) {
	e := newAccessEnv(t)
	member, id := e.user("member", "accepted", nil)
	if _, err := e.database.Exec(`UPDATE users SET level_id = 3, level_actual_amount = '1000' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	stored := func() string {
		t.Helper()
		var amount string
		if err := e.database.QueryRow(`SELECT level_actual_amount FROM users WHERE id = ?`, id).Scan(&amount); err != nil {
			t.Fatal(err)
		}
		return amount
	}

	for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "1e308", "1000abc", "999", "-1000", "300000", ""} {
		form := "action=update_custom_fee&custom_fee_amount=" + url.QueryEscape(bad)
		if rec := e.do(member, http.MethodPost, "/profile", form); rec.Code != http.StatusBadRequest {
			t.Errorf("custom fee %q = %d, want 400", bad, rec.Code)
		}
		if got := stored(); got != "1000" {
			t.Fatalf("custom fee %q was stored as %q", bad, got)
		}
	}

	if rec := e.do(member, http.MethodPost, "/profile", "action=update_custom_fee&custom_fee_amount=1500"); rec.Code != http.StatusSeeOther {
		t.Fatalf("custom fee 1500 = %d, want 303", rec.Code)
	}
	if got := stored(); got != "1500" {
		t.Errorf("custom fee 1500 stored as %q", got)
	}
}

// Admins type amounts too: a level of "NaN" would make every fee on it free.
func TestAdminAmountsMustBePlainAmounts(t *testing.T) {
	e := newAccessEnv(t)
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	_, memberID := e.user("member", "accepted", nil)

	r := e.router.(*chi.Mux)
	r.Post("/api/admin/levels", e.h.RequireAdmin(e.h.AdminCreateLevelHandler))
	r.Post("/api/admin/levels/update", e.h.RequireAdmin(e.h.AdminUpdateLevelHandler))
	r.Post("/api/admin/users/{id}/fees", e.h.RequireAdmin(e.h.AdminCreateFeeHandler))

	for _, bad := range []string{"NaN", "Inf", "1000abc", "1 000", "-5", "1e3"} {
		if rec := e.do(admin, http.MethodPost, "/api/admin/levels", `{"name":"Bad `+bad+`","amount":"`+bad+`"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("create level with amount %q = %d, want 400", bad, rec.Code)
		}
		if rec := e.do(admin, http.MethodPost, "/api/admin/levels/update", `{"id":3,"name":"Regular","amount":"`+bad+`"}`); rec.Code != http.StatusBadRequest {
			t.Errorf("update level to amount %q = %d, want 400", bad, rec.Code)
		}
		if rec := e.do(admin, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/fees", memberID), `{"period_start":"2026-03","amount":"`+bad+`","level_id":3}`); rec.Code != http.StatusBadRequest {
			t.Errorf("manual fee of %q = %d, want 400", bad, rec.Code)
		}
	}

	var levels, fees int
	e.database.QueryRow(`SELECT COUNT(*) FROM levels WHERE name LIKE 'Bad %'`).Scan(&levels)
	e.database.QueryRow(`SELECT COUNT(*) FROM fees WHERE user_id = ?`, memberID).Scan(&fees)
	if levels != 0 || fees != 0 {
		t.Errorf("invalid amounts were stored: %d levels, %d fees", levels, fees)
	}
	var regular string
	e.database.QueryRow(`SELECT amount FROM levels WHERE id = 3`).Scan(&regular)
	if regular != "1000" {
		t.Errorf("level 3 amount changed to %q", regular)
	}

	// A valid amount is stored normalised, not as typed.
	if rec := e.do(admin, http.MethodPost, "/api/admin/levels", `{"name":"Patron","amount":" 3000 "}`); rec.Code != http.StatusOK {
		t.Fatalf("create level 3000 = %d: %s", rec.Code, rec.Body.String())
	}
	var patron string
	e.database.QueryRow(`SELECT amount FROM levels WHERE name = 'Patron'`).Scan(&patron)
	if patron != "3000" {
		t.Errorf("level stored with amount %q, want 3000", patron)
	}
	if rec := e.do(admin, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/fees", memberID), `{"period_start":"2026-03","amount":"0","level_id":3}`); rec.Code != http.StatusOK {
		t.Errorf("manual fee of 0 = %d: %s", rec.Code, rec.Body.String())
	}
}
