package auth

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gorilla/sessions"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

func testAuthenticator(t *testing.T) (*Authenticator, *sql.DB) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SessionSecret: "session-test-secret", BaseURL: "https://portal.base48.cz"}
	return NewWithStore(cfg, NewSessionStore(cfg), db.New(database)), database
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
	a, _ := testAuthenticator(t)
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
			s.Values[sessionIDKey] = "session-" + issued.String()
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

	// What a cookie from before these changes holds: a user, nothing else.
	legacy := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error {
		s, _ := a.store.New(r, sessionName)
		s.Values[sessionUserKey] = user
		return s.Save(r, w)
	})
	if got := a.GetUser(requestWith(legacy)); got != nil {
		t.Error("session without a sign-in time was accepted")
	}
}

// Logout used to only ask the browser to delete the cookie; the pentest
// reused the original cookie right after and got its profile (F-04).
func TestLogoutRevokesTheSession(t *testing.T) {
	a, database := testAuthenticator(t)
	user := &User{ID: "kc-1", Email: "member@base48.cz"}
	signIn := func(w http.ResponseWriter, r *http.Request) error { return a.SignIn(w, r, user) }
	laptop := cookieFor(t, signIn)
	phone := cookieFor(t, signIn)

	// An old revocation that has expired, for logout to clean up.
	if _, err := database.Exec(`INSERT INTO revoked_sessions (id, expires_at) VALUES ('old', ?)`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	logout := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	logout.AddCookie(laptop)
	rec := httptest.NewRecorder()
	a.LogoutHandler(rec, logout)

	if got := a.GetUser(requestWith(laptop)); got != nil {
		t.Error("the logged-out session's cookie still works")
	}
	if got := a.GetUser(requestWith(phone)); got == nil {
		t.Error("logging out on one device ended the session on another")
	}

	var ids []string
	rows, err := database.Query(`SELECT id FROM revoked_sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] == "old" {
		t.Errorf("revoked_sessions = %v, want just the laptop session", ids)
	}

	// A second logout with the same cookie is harmless.
	a.LogoutHandler(httptest.NewRecorder(), logout)
}

// Sign-in hands out a new session id each time.
func TestSignInStartsANewSession(t *testing.T) {
	a, _ := testAuthenticator(t)
	ids := map[string]bool{}
	for i := 0; i < 3; i++ {
		c := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error { return a.SignIn(w, r, &User{ID: "kc-1"}) })
		r := requestWith(c)
		s, err := a.store.Get(r, sessionName)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := s.Values[sessionIDKey].(string)
		if id == "" || ids[id] {
			t.Fatalf("session id %q is empty or repeated", id)
		}
		ids[id] = true
	}
}

// The cookie used to be base64 of gob: e-mail, name and roles in plain text
// for anyone holding a copy (pentest F-07).
func TestSessionCookieIsEncrypted(t *testing.T) {
	a, _ := testAuthenticator(t)
	user := &User{ID: "kc-1", Email: "secret.member@base48.cz", Name: "Tajný Člen", Roles: []string{"memberportal_admin"}}
	c := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error { return a.SignIn(w, r, user) })

	// securecookie: base64("timestamp|base64(payload)|mac")
	outer, err := base64.URLEncoding.DecodeString(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.SplitN(outer, []byte("|"), 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected cookie format: %q", outer)
	}
	payload, err := base64.URLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{user.Email, user.Name, "memberportal_admin"} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Errorf("cookie payload contains %q in plain text", secret)
		}
	}
	if got := a.GetUser(requestWith(c)); got == nil || got.Email != user.Email {
		t.Errorf("GetUser on the encrypted cookie = %v", got)
	}

	// A cookie signed the old way, with the raw secret and no encryption,
	// does not decode.
	old := sessions.NewCookieStore([]byte("session-test-secret"))
	legacy := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error {
		s, _ := old.New(r, sessionName)
		s.Values[sessionUserKey] = user
		s.Values[sessionIssuedKey] = time.Now().Unix()
		s.Values[sessionIDKey] = "legacy"
		return s.Save(r, w)
	})
	if got := a.GetUser(requestWith(legacy)); got != nil {
		t.Error("a cookie signed with the raw secret was accepted")
	}
}

// switchView runs ViewAsMember on a request carrying c and returns the cookie
// it saved.
func switchView(t *testing.T, a *Authenticator, c *http.Cookie, on bool) (*http.Cookie, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := a.ViewAsMember(rec, requestWith(c), on); err != nil {
		return nil, err
	}
	for _, saved := range rec.Result().Cookies() {
		if saved.Name == sessionName {
			return saved, nil
		}
	}
	t.Fatal("no session cookie set")
	return nil, nil
}

// The member view hides the admin role and nothing else, and switching back
// needs nothing but the session.
func TestMemberViewHidesOnlyTheAdminRole(t *testing.T) {
	a, _ := testAuthenticator(t)
	admin := &User{ID: "kc-1", Email: "admin@base48.cz", Roles: []string{"active_member", "memberportal_admin", "in_debt"}}
	signedIn := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error { return a.SignIn(w, r, admin) })

	asMember, err := switchView(t, a, signedIn, true)
	if err != nil {
		t.Fatalf("switch to the member view: %v", err)
	}
	got := a.GetUser(requestWith(asMember))
	if got == nil || got.IsAdmin() || !got.ViewingAsMember() {
		t.Fatalf("member view: GetUser = %+v, want no admin role and the member view", got)
	}
	if !got.IsActiveMember() || !got.IsInDebt() || got.Email != admin.Email {
		t.Errorf("member view lost more than the admin role: %+v", got)
	}

	back, err := switchView(t, a, asMember, false)
	if err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if got := a.GetUser(requestWith(back)); got == nil || !got.IsAdmin() || got.ViewingAsMember() {
		t.Errorf("after switching back: GetUser = %+v, want the admin", got)
	}

	// A new sign-in in the same browser starts as admin.
	resigned := cookieFor(t, func(w http.ResponseWriter, _ *http.Request) error {
		return a.SignIn(w, requestWith(asMember), admin)
	})
	if got := a.GetUser(requestWith(resigned)); got == nil || !got.IsAdmin() || got.ViewingAsMember() {
		t.Errorf("after signing in again: GetUser = %+v, want the admin", got)
	}
}

func TestMemberViewIsForSignedInAdmins(t *testing.T) {
	a, _ := testAuthenticator(t)
	member := cookieFor(t, func(w http.ResponseWriter, r *http.Request) error {
		return a.SignIn(w, r, &User{ID: "kc-2", Roles: []string{"active_member"}})
	})
	if _, err := switchView(t, a, member, true); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("member switching to the member view: err = %v, want ErrNotAdmin", err)
	}
	stranger := &http.Cookie{Name: sessionName, Value: "garbage"}
	if _, err := switchView(t, a, stranger, true); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("no session: err = %v, want ErrNotSignedIn", err)
	}
}
