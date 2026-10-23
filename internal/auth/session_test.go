package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/base48/member-portal/internal/config"
)

func testAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	cfg := &config.Config{SessionSecret: "session-test-secret", BaseURL: "https://portal.base48.cz"}
	return NewWithStore(cfg, NewSessionStore(cfg))
}

// cookieFor runs save against a recorder and returns the session cookie it set.
func cookieFor(t *testing.T, save func(w http.ResponseWriter, r *http.Request) error) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := save(rec, httptest.NewRequest(http.MethodGet, "/", nil)); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionName {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func requestWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/profile", nil)
	r.AddCookie(c)
	return r
}

// A copy of the cookie used to work for 30 days although the browser dropped
// it after an hour (pentest F-04).
func TestSessionEndsAfterItsLifetime(t *testing.T) {
	a := testAuthenticator(t)
	user := &User{ID: "kc-1", Email: "member@base48.cz", Roles: []string{"memberportal_admin"}}

	fresh := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error { return a.SignIn(w, r, user) })
	if fresh.MaxAge != 3600 {
		t.Errorf("cookie Max-Age = %d, want 3600", fresh.MaxAge)
	}
	if got := a.GetUser(requestWith(fresh)); got == nil || got.ID != "kc-1" {
		t.Fatalf("fresh session: GetUser = %v", got)
	}

	signedInAt := func(issued time.Time) *http.Cookie {
		return cookieFor(t, func(w http.ResponseWriter, r *http.Request) error {
			s, _ := a.store.New(r, sessionName)
			s.Values[sessionUserKey] = user
			s.Values[sessionIssuedKey] = issued.Unix()
			return s.Save(r, w)
		})
	}
	if got := a.GetUser(requestWith(signedInAt(time.Now().Add(-59 * time.Minute)))); got == nil {
		t.Error("session signed in 59 minutes ago was refused")
	}
	if got := a.GetUser(requestWith(signedInAt(time.Now().Add(-61 * time.Minute)))); got != nil {
		t.Error("session signed in 61 minutes ago was accepted")
	}
	if got := a.GetUser(requestWith(signedInAt(time.Now().Add(-30 * 24 * time.Hour)))); got != nil {
		t.Error("session signed in 30 days ago was accepted")
	}

	// What a cookie from before this change holds: a user and no sign-in time.
	legacy := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error {
		s, _ := a.store.New(r, sessionName)
		s.Values[sessionUserKey] = user
		return s.Save(r, w)
	})
	if got := a.GetUser(requestWith(legacy)); got != nil {
		t.Error("session without a sign-in time was accepted")
	}
}
