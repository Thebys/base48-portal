package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Nothing stopped a member from booking a bay for years (pentest F-08).
func TestMemberReservationsHaveLimits(t *testing.T) {
	e := newAccessEnv(t)
	bay := e.bay()
	member, _ := e.user("member", "accepted", nil)
	admin, adminID := e.user("admin", "accepted", []string{"memberportal_admin"})

	book := func(fromHours, toHours int, note string) *jsonResult {
		start, end := slot(fromHours, toHours)
		rec := e.do(member, http.MethodPost, "/api/member/workshop/reservations",
			fmt.Sprintf(`{"resource_id":%d,"starts_at":%q,"ends_at":%q,"note":%q}`, bay, start, end, note))
		var res jsonResult
		json.Unmarshal(rec.Body.Bytes(), &res)
		res.code = rec.Code
		return &res
	}
	const day = 24

	if r := book(1, 1+32*day, ""); r.code != http.StatusBadRequest {
		t.Errorf("32-day reservation = %d, want 400", r.code)
	}
	if r := book(1, 1+10*365*day, ""); r.code != http.StatusBadRequest {
		t.Errorf("10-year reservation = %d, want 400", r.code)
	}
	if r := book(200*day, 200*day+2, ""); r.code != http.StatusBadRequest {
		t.Errorf("reservation 200 days ahead = %d, want 400", r.code)
	}
	if r := book(1, 3, strings.Repeat("ž", 201)); r.code != http.StatusBadRequest {
		t.Errorf("201-character note = %d, want 400", r.code)
	}

	// 30 days, not 31: slot() adds real hours, and across a switch to
	// summer time 31 days of them are 31 days and an hour on the clock.
	month := book(1, 1+30*day, strings.Repeat("ž", 200))
	if month.code != http.StatusOK {
		t.Fatalf("30-day reservation with a 200-character note = %d (%s), want 200", month.code, month.Error)
	}
	if r := book(170*day, 170*day+2, ""); r.code != http.StatusOK {
		t.Errorf("reservation 170 days ahead = %d (%s), want 200", r.code, r.Error)
	}

	// Extending counts from the reservation's start.
	extend := func(id int64, toHours int) int {
		_, end := slot(0, toHours)
		return e.do(member, http.MethodPost, fmt.Sprintf("/api/member/workshop/reservations/%d/end", id),
			fmt.Sprintf(`{"ends_at":%q}`, end)).Code
	}
	if code := extend(month.ID, 1+40*day); code != http.StatusBadRequest {
		t.Errorf("extending to 40 days = %d, want 400", code)
	}
	if code := extend(month.ID, 1+20*day); code != http.StatusOK {
		t.Errorf("shortening to 20 days = %d, want 200", code)
	}

	// Admins book without the limits, e.g. a car left for the summer.
	start, end := slot(25*day, 25*day+90*day)
	if rec := e.do(admin, http.MethodPost, "/api/admin/workshop/reservations",
		fmt.Sprintf(`{"resource_id":%d,"user_id":%d,"starts_at":%q,"ends_at":%q}`, bay, adminID, start, end)); rec.Code != http.StatusOK {
		t.Errorf("admin 90-day reservation = %d: %s", rec.Code, rec.Body.String())
	}
}

type jsonResult struct {
	code  int
	ID    int64  `json:"id"`
	Error string `json:"error"`
}
