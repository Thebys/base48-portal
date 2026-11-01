package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// viewAs posts the member view switch from the page referer with cookie c
// and returns the response.
func (e *accessEnv) viewAs(c *http.Cookie, as, referer string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/view-as", strings.NewReader("as="+as))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", referer)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *accessEnv) get(c *http.Cookie, path string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func savedCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("no cookie saved (status %d, body %.200q)", rec.Code, rec.Body.String())
	}
	return cookies[0]
}

// An admin in the member view gets what a member gets: no admin pages or
// APIs, no Správa menu, and a way back.
func TestAdminCanViewThePortalAsAMember(t *testing.T) {
	e := newAccessEnv(t)
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	signedIn := e.sessionCookie(admin)

	if rec := e.get(signedIn, "/admin/emails"); rec.Code != http.StatusOK {
		t.Fatalf("admin before switching: GET /admin/emails = %d", rec.Code)
	}

	rec := e.viewAs(signedIn, "member", "https://portal.base48.cz/admin/finance")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/finance" {
		t.Fatalf("switch to member view = %d to %q, want 303 to /finance", rec.Code, rec.Header().Get("Location"))
	}
	asMember := savedCookie(t, rec)

	for _, path := range []string{"/admin/emails", "/admin/finance", "/api/admin/users"} {
		if rec := e.get(asMember, path); rec.Code != http.StatusForbidden {
			t.Errorf("member view: GET %s = %d, want 403", path, rec.Code)
		}
	}
	profile := e.get(asMember, "/profile")
	if profile.Code != http.StatusOK {
		t.Fatalf("member view: GET /profile = %d", profile.Code)
	}
	body := profile.Body.String()
	for _, adminOnly := range []string{`href="/admin/users"`, `href="/admin/finance"`, "Zobrazit jako člen"} {
		if strings.Contains(body, adminOnly) {
			t.Errorf("member view profile shows %q", adminOnly)
		}
	}
	if !strings.Contains(body, "member-view-bar") || !strings.Contains(body, `href="/finance"`) {
		t.Error("member view profile lacks the member-view bar or the member finance link")
	}

	rec = e.viewAs(asMember, "admin", "https://portal.base48.cz/finance")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/finance" {
		t.Fatalf("switch back = %d to %q, want 303 to /admin/finance", rec.Code, rec.Header().Get("Location"))
	}
	back := savedCookie(t, rec)
	if rec := e.get(back, "/admin/emails"); rec.Code != http.StatusOK {
		t.Errorf("after switching back: GET /admin/emails = %d, want 200", rec.Code)
	}
	if body := e.get(back, "/profile").Body.String(); strings.Contains(body, "member-view-bar") || !strings.Contains(body, "Zobrazit jako člen") {
		t.Error("after switching back the profile still looks like the member view")
	}
}

func TestOnlyAdminsSwitchToTheMemberView(t *testing.T) {
	e := newAccessEnv(t)
	member, _ := e.user("member", "accepted", nil)
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})

	if rec := e.viewAs(e.sessionCookie(member), "member", "/profile"); rec.Code != http.StatusForbidden {
		t.Errorf("member switching to the member view = %d, want 403", rec.Code)
	}
	if body := e.get(e.sessionCookie(member), "/profile").Body.String(); strings.Contains(body, "/view-as") {
		t.Error("a member's profile offers the view switch")
	}
	if rec := e.viewAs(e.sessionCookie(admin), "root", "/profile"); rec.Code != http.StatusBadRequest {
		t.Errorf("as=root = %d, want 400", rec.Code)
	}
}

func TestViewAsTarget(t *testing.T) {
	tests := []struct {
		referer  string
		asMember bool
		want     string
	}{
		{"https://portal.base48.cz/admin/finance", true, "/finance"},
		{"https://portal.base48.cz/admin/projects", true, "/projects"},
		{"https://portal.base48.cz/admin/emails/templates?t=bar_debt", true, "/profile"},
		{"https://portal.base48.cz/admin", true, "/profile"},
		{"https://portal.base48.cz/workshop?week=2026-11-02", true, "/workshop?week=2026-11-02"},
		{"https://portal.base48.cz/administrace", true, "/administrace"},
		{"https://portal.base48.cz/finance", false, "/admin/finance"},
		{"https://portal.base48.cz/projects", false, "/admin/projects"},
		{"https://portal.base48.cz/profile", false, "/profile"},
		{"https://portal.base48.cz/a%20b", false, "/a%20b"},
		// Only the path counts, and only a path that stays on the portal.
		{"https://evil.example/workshop", true, "/workshop"},
		{"https://portal.base48.cz//evil.example/x", true, "/profile"},
		{`https://portal.base48.cz/\evil.example`, false, "/profile"},
		{"https://portal.base48.cz/%2F%2Fevil.example", false, "/profile"},
		{"", true, "/profile"},
		{"::not a url", false, "/profile"},
	}
	for _, tt := range tests {
		if got := viewAsTarget(tt.referer, tt.asMember); got != tt.want {
			t.Errorf("viewAsTarget(%q, %v) = %q, want %q", tt.referer, tt.asMember, got, tt.want)
		}
	}
}
