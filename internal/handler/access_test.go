package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/sessions"

	"github.com/base48/member-portal/internal/access"
	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/email"
	"github.com/base48/member-portal/internal/qrpay"
)

// accessEnv is a handler on a throwaway database with a session store that
// needs no Keycloak, wired to the routes the access rules guard.
type accessEnv struct {
	t        *testing.T
	h        *Handler
	sessions *sessions.CookieStore
	database *sql.DB
	router   http.Handler
	nextVS   int
}

func newAccessEnv(t *testing.T) *accessEnv {
	t.Helper()
	queries, database := testQueries(t)
	cfg := &config.Config{WebRoot: "../../web", BaseURL: "https://portal.base48.cz"}
	store := sessions.NewCookieStore([]byte("access-test-session-key"))
	h := &Handler{
		auth:         auth.NewWithStore(cfg, store),
		db:           database,
		queries:      queries,
		config:       cfg,
		emailClient:  email.New(cfg, queries, qrpay.NewService("", "")),
		qrpayService: qrpay.NewService("", ""),
		webRoot:      "../../web",
	}

	// Same paths as cmd/server/main.go.
	r := chi.NewRouter()
	r.Get("/profile", h.ProfileHandler)
	r.Post("/profile", h.ProfileHandler)
	r.Get("/services", h.ServicesHandler)
	r.Get("/finance", h.MemberFinanceHandler)
	r.Get("/payments/recent", h.MemberRecentPaymentsHandler)
	r.Get("/projects", h.MemberProjectsHandler)
	r.Get("/workshop", h.WorkshopHandler)
	r.Get("/api/member/projects", h.MemberProjectsAPIHandler)
	r.Get("/api/member/projects/payments", h.MemberProjectPaymentsHandler)
	r.Get("/api/member/workshop", h.WorkshopAPIHandler)
	r.Post("/api/member/workshop/reservations", h.CreateReservationHandler)
	r.Post("/api/member/workshop/reservations/{id}/cancel", h.CancelReservationHandler)
	r.Post("/api/member/workshop/reservations/{id}/end", h.UpdateReservationEndHandler)
	r.Get("/api/admin/users", h.RequireAdmin(h.AdminUsersAPIHandler))
	r.Post("/api/admin/users/{id}/state", h.RequireAdmin(h.AdminUpdateUserStateHandler))
	r.Post("/api/admin/users/{id}/access", h.RequireAdmin(h.AdminGrantAccessHandler))
	r.Delete("/api/admin/users/{id}/access/{scope}", h.RequireAdmin(h.AdminRevokeAccessHandler))
	r.Post("/api/admin/workshop/reservations", h.RequireAdmin(h.AdminCreateReservationHandler))

	return &accessEnv{t: t, h: h, sessions: store, database: database, router: r}
}

// user creates a portal user who has already signed in once (so
// getOrCreateUser finds them by Keycloak ID) and returns their session
// identity and id.
func (e *accessEnv) user(name, state string, roles []string, scopes ...access.Scope) (*auth.User, int64) {
	e.t.Helper()
	e.nextVS++
	var id int64
	err := e.database.QueryRow(
		`INSERT INTO users (keycloak_id, email, username, level_id, payments_id, state)
		 VALUES (?, ?, ?, 1, ?, ?) RETURNING id`,
		"kc-"+name, name+"@base48.cz", name, fmt.Sprintf("9%04d", e.nextVS), state,
	).Scan(&id)
	if err != nil {
		e.t.Fatalf("insert user %s: %v", name, err)
	}
	for _, scope := range scopes {
		if _, err := e.database.Exec(`INSERT INTO access_grants (user_id, scope) VALUES (?, ?)`, id, string(scope)); err != nil {
			e.t.Fatalf("grant %s to %s: %v", scope, name, err)
		}
	}
	return &auth.User{ID: "kc-" + name, Email: name + "@base48.cz", PreferredName: name, Roles: roles}, id
}

func (e *accessEnv) do(u *auth.User, method, target, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/json")
	} else if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(e.sessionCookie(u))
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// sessionCookie signs u in: the cookie GetUser reads them back from.
func (e *accessEnv) sessionCookie(u *auth.User) *http.Cookie {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	session, err := e.sessions.New(req, auth.SessionName)
	if err != nil {
		e.t.Fatalf("new session: %v", err)
	}
	session.Values[auth.SessionUserKey] = u
	if err := session.Save(req, rec); err != nil {
		e.t.Fatalf("save session: %v", err)
	}
	return rec.Result().Cookies()[0]
}

// slot is a reservation window starting the given number of hours from now,
// in the datetime-local format the API takes.
func slot(fromHours, toHours int) (string, string) {
	now := time.Now()
	return now.Add(time.Duration(fromHours) * time.Hour).Format("2006-01-02T15:04"),
		now.Add(time.Duration(toHours) * time.Hour).Format("2006-01-02T15:04")
}

func (e *accessEnv) bay() int64 {
	e.t.Helper()
	var id int64
	if err := e.database.QueryRow(`SELECT id FROM resources WHERE slug = 'bay-1'`).Scan(&id); err != nil {
		e.t.Fatalf("bay-1: %v", err)
	}
	return id
}

func TestWhoGetsWhichPage(t *testing.T) {
	e := newAccessEnv(t)

	member, _ := e.user("member", "accepted", nil)
	applicant, _ := e.user("applicant", "awaiting", nil)
	external, _ := e.user("external", "external", nil)
	granted, _ := e.user("granted", "external", nil, access.Workshop)
	exmember, _ := e.user("exmember", "exmember", nil)
	suspended, _ := e.user("suspended", "suspended", nil)
	rejected, _ := e.user("rejected", "rejected", nil)
	admin, _ := e.user("admin", "awaiting", []string{"memberportal_admin"})

	const ok, redirect, forbidden = http.StatusOK, http.StatusTemporaryRedirect, http.StatusForbidden

	tests := []struct {
		path string
		want map[*auth.User]int
	}{
		{"/profile", map[*auth.User]int{
			member: ok, applicant: ok, external: ok, granted: ok, exmember: ok, suspended: ok, rejected: ok, admin: ok}},
		{"/finance", map[*auth.User]int{
			member: ok, applicant: redirect, external: redirect, granted: redirect, exmember: redirect, suspended: redirect, rejected: redirect, admin: ok}},
		{"/payments/recent", map[*auth.User]int{
			member: ok, applicant: redirect, external: redirect, granted: redirect, exmember: redirect, suspended: redirect, rejected: redirect, admin: ok}},
		{"/services", map[*auth.User]int{
			member: ok, applicant: redirect, external: redirect, granted: redirect, exmember: redirect, suspended: redirect, rejected: redirect, admin: ok}},
		{"/projects", map[*auth.User]int{
			member: ok, applicant: redirect, external: ok, granted: ok, exmember: redirect, suspended: redirect, rejected: redirect, admin: ok}},
		{"/api/member/projects", map[*auth.User]int{
			member: ok, applicant: forbidden, external: ok, granted: ok, exmember: forbidden, suspended: forbidden, rejected: forbidden, admin: ok}},
		{"/api/member/projects/payments?project_id=1", map[*auth.User]int{
			member: ok, applicant: forbidden, external: ok, granted: ok, exmember: forbidden, suspended: forbidden, rejected: forbidden, admin: ok}},
		{"/workshop", map[*auth.User]int{
			member: ok, applicant: redirect, external: redirect, granted: ok, exmember: redirect, suspended: redirect, rejected: redirect, admin: ok}},
		{"/api/member/workshop", map[*auth.User]int{
			member: ok, applicant: forbidden, external: forbidden, granted: ok, exmember: forbidden, suspended: forbidden, rejected: forbidden, admin: ok}},
	}

	for _, tt := range tests {
		for u, want := range tt.want {
			rec := e.do(u, http.MethodGet, tt.path, "")
			if rec.Code != want {
				t.Errorf("GET %s as %s = %d, want %d (body %.200q)", tt.path, u.PreferredName, rec.Code, want, rec.Body.String())
				continue
			}
			if want == redirect && rec.Header().Get("Location") != "/profile" {
				t.Errorf("GET %s as %s redirects to %q, want /profile", tt.path, u.PreferredName, rec.Header().Get("Location"))
			}
		}
	}
}

func TestNavigationFollowsAccess(t *testing.T) {
	e := newAccessEnv(t)

	member, _ := e.user("member", "accepted", nil)
	external, _ := e.user("external", "external", nil)
	granted, _ := e.user("granted", "external", nil, access.Workshop)
	exmember, _ := e.user("exmember", "exmember", nil)
	applicant, _ := e.user("applicant", "awaiting", nil)

	links := []string{`href="/finance"`, `href="/projects"`, `href="/workshop"`}
	tests := []struct {
		user *auth.User
		want []bool // finance, projects, workshop
	}{
		{member, []bool{true, true, true}},
		{external, []bool{false, true, false}},
		{granted, []bool{false, true, true}},
		{exmember, []bool{false, false, false}},
		{applicant, []bool{false, false, false}},
	}
	for _, tt := range tests {
		body := e.do(tt.user, http.MethodGet, "/profile", "").Body.String()
		for i, link := range links {
			// Once in the desktop menu, once in the mobile one.
			want := 0
			if tt.want[i] {
				want = 2
			}
			if got := strings.Count(body, link); got != want {
				t.Errorf("profile of %s: menu has %s %d times, want %d", tt.user.PreferredName, link, got, want)
			}
		}
	}
}

func TestProfileByState(t *testing.T) {
	e := newAccessEnv(t)

	granted, _ := e.user("granted", "external", nil, access.Workshop)
	external, _ := e.user("external", "external", nil)
	applicant, _ := e.user("applicant", "awaiting", nil)
	suspended, _ := e.user("suspended", "suspended", nil)
	member, _ := e.user("member", "accepted", nil)

	tests := []struct {
		user        *auth.User
		has, hasNot []string
	}{
		{granted,
			[]string{"Nejsi členem Base48", "s přístupem do: Autodílna", "Úrovně členství"},
			[]string{"čeká na schválení", "Příchozí platby", "Nastavení výše příspěvku", "Změna úrovně členství", "QR kód pro členský příspěvek"}},
		{external,
			[]string{"Nejsi členem Base48", "Máš u nás externí účet."},
			[]string{"s přístupem do", "Příchozí platby"}},
		{applicant,
			[]string{"Tvoje registrace čeká na schválení", "Úrovně členství"},
			[]string{"Nejsi členem Base48", "Nastavení výše příspěvku"}},
		{suspended,
			[]string{"Členství a platby", "Příchozí platby"},
			[]string{"Nastavení výše příspěvku", "Změna úrovně členství", "Nejsi členem Base48"}},
		{member,
			[]string{"Členství a platby", "Nastavení výše příspěvku"},
			[]string{"Nejsi členem Base48", "čeká na schválení"}},
	}
	for _, tt := range tests {
		rec := e.do(tt.user, http.MethodGet, "/profile", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("profile of %s = %d: %s", tt.user.PreferredName, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, s := range tt.has {
			if !strings.Contains(body, s) {
				t.Errorf("profile of %s lacks %q", tt.user.PreferredName, s)
			}
		}
		for _, s := range tt.hasNot {
			if strings.Contains(body, s) {
				t.Errorf("profile of %s shows %q", tt.user.PreferredName, s)
			}
		}
	}
}

func TestMembershipSettingsOnlyForMembers(t *testing.T) {
	e := newAccessEnv(t)

	for _, state := range []string{"external", "awaiting", "suspended", "exmember"} {
		u, id := e.user(state, state, nil)
		for _, form := range []string{"action=update_custom_fee&custom_fee_amount=5000", "action=upgrade_level&new_level_id=2"} {
			rec := e.do(u, http.MethodPost, "/profile", form)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST /profile %s as %s = %d, want 403", form, state, rec.Code)
			}
		}
		var level int64
		var amount string
		e.database.QueryRow(`SELECT level_id, level_actual_amount FROM users WHERE id = ?`, id).Scan(&level, &amount)
		if level != 1 || amount != "0" {
			t.Errorf("%s changed their membership anyway: level %d, amount %s", state, level, amount)
		}
	}

	member, _ := e.user("member", "accepted", nil)
	if rec := e.do(member, http.MethodPost, "/profile", "action=upgrade_level&new_level_id=2"); rec.Code != http.StatusSeeOther {
		t.Errorf("member upgrade = %d, want 303", rec.Code)
	}
}

func TestWorkshopReservationsFollowGrants(t *testing.T) {
	e := newAccessEnv(t)
	bay := e.bay()

	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	granted, grantedID := e.user("granted", "external", nil, access.Workshop)
	external, externalID := e.user("external", "external", nil)

	reserve := func(u *auth.User, from, to int) *httptest.ResponseRecorder {
		start, end := slot(from, to)
		return e.do(u, http.MethodPost, "/api/member/workshop/reservations",
			fmt.Sprintf(`{"resource_id":%d,"starts_at":%q,"ends_at":%q,"note":"brzdy"}`, bay, start, end))
	}

	// Without a grant: no.
	if rec := reserve(external, 24, 26); rec.Code != http.StatusForbidden {
		t.Errorf("reservation by external without grant = %d, want 403", rec.Code)
	}

	// With one: yes, and it is logged like any member's.
	rec := reserve(granted, 24, 26)
	if rec.Code != http.StatusOK {
		t.Fatalf("reservation by granted = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	var logged int
	e.database.QueryRow(`SELECT count(*) FROM system_logs WHERE subsystem = 'workshop' AND user_id = ? AND message LIKE 'Reservation created%'`, grantedID).Scan(&logged)
	if logged != 1 {
		t.Errorf("granted's reservation logged %d times, want 1", logged)
	}

	// Admin booking on someone's behalf follows the same rule.
	start, end := slot(48, 50)
	onBehalf := func(userID int64) int {
		return e.do(admin, http.MethodPost, "/api/admin/workshop/reservations",
			fmt.Sprintf(`{"resource_id":%d,"user_id":%d,"starts_at":%q,"ends_at":%q}`, bay, userID, start, end)).Code
	}
	if code := onBehalf(externalID); code != http.StatusBadRequest {
		t.Errorf("admin booking for external without grant = %d, want 400", code)
	}
	if code := onBehalf(grantedID); code != http.StatusOK {
		t.Errorf("admin booking for granted = %d, want 200", code)
	}

	// The admin's member picker offers exactly those who may book.
	var list struct {
		Users []struct {
			Username       string `json:"username"`
			WorkshopAccess bool   `json:"workshop_access"`
		} `json:"users"`
	}
	json.Unmarshal(e.do(admin, http.MethodGet, "/api/admin/users", "").Body.Bytes(), &list)
	want := map[string]bool{"admin": true, "granted": true, "external": false}
	for _, u := range list.Users {
		if u.WorkshopAccess != want[u.Username] {
			t.Errorf("admin users API: %s workshop_access = %v, want %v", u.Username, u.WorkshopAccess, want[u.Username])
		}
	}

	// Revoking: no more extending, but the slot can still be given back.
	if rec := e.do(admin, http.MethodDelete, fmt.Sprintf("/api/admin/users/%d/access/workshop", grantedID), ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", rec.Code, rec.Body.String())
	}
	_, later := slot(24, 28)
	if rec := e.do(granted, http.MethodPost, fmt.Sprintf("/api/member/workshop/reservations/%d/end", created.ID),
		fmt.Sprintf(`{"ends_at":%q}`, later)); rec.Code != http.StatusForbidden {
		t.Errorf("extending after revoke = %d, want 403", rec.Code)
	}
	if rec := e.do(granted, http.MethodPost, fmt.Sprintf("/api/member/workshop/reservations/%d/cancel", created.ID), ""); rec.Code != http.StatusOK {
		t.Errorf("cancelling after revoke = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminGrantsAccess(t *testing.T) {
	e := newAccessEnv(t)

	admin, adminID := e.user("admin", "accepted", []string{"memberportal_admin"})
	member, _ := e.user("member", "accepted", nil)
	granted, grantedID := e.user("granted", "awaiting", nil)
	grantURL := fmt.Sprintf("/api/admin/users/%d/access", grantedID)

	// Only admins.
	if rec := e.do(member, http.MethodPost, grantURL, `{"scope":"workshop"}`); rec.Code != http.StatusForbidden {
		t.Errorf("grant by non-admin = %d, want 403", rec.Code)
	}

	// Applicant → external, then the workshop.
	if rec := e.do(admin, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/state", grantedID), `{"state":"external"}`); rec.Code != http.StatusOK {
		t.Fatalf("state → external = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(admin, http.MethodPost, grantURL, `{"scope":"lift"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown scope = %d, want 400", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, grantURL, `{"scope":"workshop","note":"  dohoda s radou  "}`); rec.Code != http.StatusOK {
		t.Fatalf("grant = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(admin, http.MethodPost, grantURL, `{"scope":"workshop"}`); rec.Code != http.StatusConflict {
		t.Errorf("second grant = %d, want 409", rec.Code)
	}

	var note string
	var grantedBy int64
	if err := e.database.QueryRow(`SELECT note, granted_by FROM access_grants WHERE user_id = ? AND scope = 'workshop'`, grantedID).Scan(&note, &grantedBy); err != nil {
		t.Fatal(err)
	}
	if note != "dohoda s radou" || grantedBy != adminID {
		t.Errorf("grant row: note %q by %d, want %q by %d", note, grantedBy, "dohoda s radou", adminID)
	}

	// The user now gets in.
	if rec := e.do(granted, http.MethodGet, "/workshop", ""); rec.Code != http.StatusOK {
		t.Errorf("workshop after grant = %d, want 200", rec.Code)
	}

	// Revoke, twice.
	revokeURL := grantURL + "/workshop"
	if rec := e.do(admin, http.MethodDelete, revokeURL, ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(admin, http.MethodDelete, revokeURL, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second revoke = %d, want 404", rec.Code)
	}
	if rec := e.do(granted, http.MethodGet, "/workshop", ""); rec.Code != http.StatusTemporaryRedirect {
		t.Errorf("workshop after revoke = %d, want 307", rec.Code)
	}

	// Both changes are in the audit log.
	var logged int
	e.database.QueryRow(`SELECT count(*) FROM system_logs WHERE subsystem = 'access' AND user_id = ?`, grantedID).Scan(&logged)
	if logged != 2 {
		t.Errorf("access log entries = %d, want 2 (grant and revoke)", logged)
	}
}

func TestNonMembersSeeNoPersonalData(t *testing.T) {
	e := newAccessEnv(t)
	bay := e.bay()

	member, _ := e.user("member", "accepted", nil)
	granted, _ := e.user("granted", "external", nil, access.Workshop)

	// A member who never set a nick: the calendar would fall back to e-mail.
	var quietID int64
	if err := e.database.QueryRow(
		`INSERT INTO users (email, level_id, state) VALUES ('quiet@base48.cz', 1, 'accepted') RETURNING id`,
	).Scan(&quietID); err != nil {
		t.Fatal(err)
	}
	start, end := slot(24, 26)
	if _, err := e.database.Exec(
		`INSERT INTO reservations (resource_id, user_id, starts_at, ends_at) VALUES (?, ?, ?, ?)`,
		bay, quietID, strings.Replace(start, "T", " ", 1), strings.Replace(end, "T", " ", 1),
	); err != nil {
		t.Fatal(err)
	}

	// A donation to a project, from a donor's own bank account.
	var projectID int64
	if err := e.database.QueryRow(`INSERT INTO projects (name) VALUES ('Zvedák') RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.database.Exec(
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, project_id)
		 VALUES ('2026-09-01', '500', 'fio', 'don-1', 'FIO', '123456789/0800', '4848', ?)`, projectID,
	); err != nil {
		t.Fatal(err)
	}

	calendar := func(u *auth.User) string {
		var resp struct {
			Reservations []struct {
				Username string `json:"username"`
			} `json:"reservations"`
		}
		rec := e.do(u, http.MethodGet, "/api/member/workshop", "")
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Reservations) != 1 {
			t.Fatalf("workshop API as %s: %d %s", u.PreferredName, rec.Code, rec.Body.String())
		}
		return resp.Reservations[0].Username
	}
	if got := calendar(member); got != "quiet@base48.cz" {
		t.Errorf("member sees the reservation as %q, want the e-mail", got)
	}
	if got := calendar(granted); strings.Contains(got, "@") {
		t.Errorf("external user sees a member's e-mail: %q", got)
	}

	donor := func(u *auth.User) string {
		var resp struct {
			Payments []struct {
				RemoteAccount  string `json:"remote_account"`
				Identification string `json:"identification"`
			} `json:"payments"`
		}
		rec := e.do(u, http.MethodGet, fmt.Sprintf("/api/member/projects/payments?project_id=%d", projectID), "")
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Payments) != 1 {
			t.Fatalf("project payments as %s: %d %s", u.PreferredName, rec.Code, rec.Body.String())
		}
		return resp.Payments[0].RemoteAccount
	}
	if got := donor(member); got != "123456789/0800" {
		t.Errorf("member sees donor account %q, want it shown", got)
	}
	if got := donor(granted); got != "" {
		t.Errorf("external user sees donor account %q", got)
	}
}
