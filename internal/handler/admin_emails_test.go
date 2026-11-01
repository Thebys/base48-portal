package handler

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// emailEnv is the access environment with a payment account, so the
// membership reminders can be built, and an admin signed in.
func emailEnv(t *testing.T) (*accessEnv, func(method, target, body string) (int, string, string)) {
	t.Helper()
	e := newAccessEnv(t)
	recordPaymentAccount(t, e.h.queries, time.Now())
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	do := func(method, target, body string) (int, string, string) {
		rec := e.do(admin, method, target, body)
		return rec.Code, rec.Body.String(), rec.Header().Get("Location")
	}
	return e, do
}

func form(values map[string]string) string {
	v := url.Values{}
	for k, s := range values {
		v.Set(k, s)
	}
	return v.Encode()
}

func TestEmailAdminIsForAdmins(t *testing.T) {
	e := newAccessEnv(t)
	member, _ := e.user("member", "accepted", nil)
	for _, target := range []string{"/admin/emails", "/admin/emails/templates", "/admin/emails/reminders"} {
		if rec := e.do(member, http.MethodGet, target, ""); rec.Code == http.StatusOK {
			t.Errorf("a member got %s", target)
		}
	}
}

// Waiting emails come first, all of them, under their Czech name; the reason
// an email was cancelled shows next to it.
func TestOutboxPage(t *testing.T) {
	e, do := emailEnv(t)
	for _, q := range []string{
		`INSERT INTO email_outbox (recipient, subject, template_name, status, next_retry_at) VALUES ('a@base48.cz', 'Připomínka', 'prepaid_ending.html', 'pending', datetime('now', '+3 days'))`,
		`INSERT INTO email_outbox (recipient, subject, template_name, status, last_error) VALUES ('b@base48.cz', 'Dluh', 'debt_warning.html', 'cancelled', 'Zrušeno před odesláním: dluh mezitím uhrazen')`,
	} {
		if _, err := e.database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	code, body, _ := do(http.MethodGet, "/admin/emails", "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, want := range []string{"Před koncem předplatného", "odejde ", "Upozornění na dluh", "dluh mezitím uhrazen", `data-outbox-action="cancel"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the outbox page lacks %q", want)
		}
	}
	if strings.Index(body, "a@base48.cz") > strings.Index(body, "b@base48.cz") {
		t.Error("the waiting email is not listed first")
	}

	_, filtered, _ := do(http.MethodGet, "/admin/emails?status=sent", "")
	if strings.Contains(filtered, "b@base48.cz") {
		t.Error("a cancelled email shows under Odeslané")
	}
}

// The stored email is shown in a sandboxed frame, escaped into srcdoc: its
// markup must not become part of the admin page.
func TestEmailDetailShowsStoredHTML(t *testing.T) {
	e, do := emailEnv(t)
	if _, err := e.database.Exec(`INSERT INTO email_outbox (id, recipient, subject, template_name, status, rendered_html)
		VALUES (7, 'a@base48.cz', 'Ahoj', 'welcome.html', 'sent', '<p onclick="x()">Vítej</p><script>alert(1)</script>')`); err != nil {
		t.Fatal(err)
	}
	code, body, _ := do(http.MethodGet, "/admin/emails/7", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, `<iframe sandbox title="Obsah e-mailu" srcdoc="&lt;p onclick=&#34;x()&#34;&gt;Vítej&lt;/p&gt;&lt;script&gt;`) {
		t.Error("the email is not in a sandboxed srcdoc, escaped")
	}
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("the email's script reached the admin page")
	}
	if code, _, _ := do(http.MethodGet, "/admin/emails/999", ""); code != http.StatusNotFound {
		t.Errorf("a missing email: %d", code)
	}
}

func overrides(t *testing.T, e *accessEnv, template, block string) []string {
	t.Helper()
	rows, err := e.database.Query(`SELECT content FROM email_template_content WHERE template_name = ? AND block_name = ? AND lang = 'cs'`, template, block)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// Náhled shows the text as typed without saving it; Uložit saves it and
// logs what it replaced; Obnovit výchozí puts the default back.
func TestTemplateEditor(t *testing.T) {
	e, do := emailEnv(t)

	code, body, _ := do(http.MethodGet, "/admin/emails/templates?t=prepaid_ending&lang=cs", "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Index(body, `name="block.subject"`) > strings.Index(body, `name="block.prepay_text"`) {
		t.Error("the blocks are not in the order the email shows them")
	}
	if i := strings.Index(body, "Předmět:</span>"); i < 0 || !strings.Contains(body[i:i+200], "listopadu") || strings.Contains(body[i:i+200], "{zaplaceno_do}") {
		t.Errorf("the preview does not show the subject with the placeholder filled in: %.200s", body[max(0, i):])
	}

	draft := map[string]string{"t": "prepaid_ending", "lang": "cs", "action": "preview"}
	for _, b := range []string{"subject", "subject_short", "preheader", "preheader_short", "heading", "heading_short", "intro_text", "intro_text_short", "prepay_text", "footer", "sign_off"} {
		draft["block."+b] = "text " + b
	}
	draft["block.heading"] = "Díky moc, {zaplaceno_do}!"
	code, body, _ = do(http.MethodPost, "/admin/emails/templates", form(draft))
	if code != http.StatusOK {
		t.Fatalf("preview: status %d", code)
	}
	if !strings.Contains(html.UnescapeString(body), "Díky moc, listopadu") {
		t.Error("the preview does not show the unsaved heading")
	}
	if !strings.Contains(body, "neuložené změny") {
		t.Error("the page does not say the preview is unsaved")
	}
	if got := overrides(t, e, "prepaid_ending", "heading"); len(got) != 0 {
		t.Fatalf("the preview saved %v", got)
	}

	draft["action"] = "save"
	code, _, location := do(http.MethodPost, "/admin/emails/templates", form(draft))
	if code != http.StatusSeeOther || !strings.Contains(location, "saved=1") {
		t.Fatalf("save: %d to %q", code, location)
	}
	if got := overrides(t, e, "prepaid_ending", "heading"); len(got) != 1 || got[0] != "Díky moc, {zaplaceno_do}!" {
		t.Fatalf("saved %v", got)
	}
	var logged string
	if err := e.database.QueryRow(`SELECT metadata FROM system_logs WHERE message LIKE 'Šablona prepaid_ending (cs)%' ORDER BY id DESC LIMIT 1`).Scan(&logged); err != nil {
		t.Fatalf("no audit line: %v", err)
	}
	if !strings.Contains(logged, "Díky za podporu Base48") || !strings.Contains(logged, "admin@base48.cz") {
		t.Errorf("the audit line lacks the replaced text or the admin: %s", logged)
	}

	code, _, _ = do(http.MethodPost, "/admin/emails/templates", form(map[string]string{"t": "prepaid_ending", "lang": "cs", "action": "reset:heading"}))
	if code != http.StatusSeeOther {
		t.Fatalf("reset: %d", code)
	}
	if got := overrides(t, e, "prepaid_ending", "heading"); len(got) != 0 {
		t.Errorf("reset left %v", got)
	}
}

// A placeholder the template does not know is refused, with nothing saved.
func TestTemplateEditorRefusesUnknownPlaceholder(t *testing.T) {
	e, do := emailEnv(t)
	code, body, _ := do(http.MethodPost, "/admin/emails/templates", form(map[string]string{
		"t": "prepaid_ending", "lang": "cs", "action": "save", "block.heading": "Do {zaplacenodo}",
	}))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "{zaplacenodo}") {
		t.Errorf("status %d; the page should name the unknown placeholder", code)
	}
	if got := overrides(t, e, "prepaid_ending", "heading"); len(got) != 0 {
		t.Errorf("saved %v", got)
	}
}

// The test email goes to the signed-in admin; there is no field for anyone
// else. Without SMTP it says so instead of claiming it went.
func TestTemplateTestEmailGoesToTheAdmin(t *testing.T) {
	e, do := emailEnv(t)
	code, body, _ := do(http.MethodPost, "/admin/emails/templates", form(map[string]string{
		"t": "debt_warning", "lang": "cs", "action": "test", "email": "member@base48.cz",
	}))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "SMTP není nastavené") {
		t.Errorf("status %d; want the SMTP error shown", code)
	}
	var recipient string
	if err := e.database.QueryRow(`SELECT message FROM system_logs WHERE message LIKE 'Testovací e-mail%' ORDER BY id DESC LIMIT 1`).Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recipient, "admin@base48.cz") || strings.Contains(recipient, "member@") {
		t.Errorf("logged %q", recipient)
	}
}

// The member preview shows the reminder the member is due now.
func TestMemberPreview(t *testing.T) {
	e, do := emailEnv(t)
	owing := seedMember(t, e.database, "owing@base48.cz", "5001", 1000, 0)
	if _, err := e.database.Exec(`INSERT INTO fees (user_id, level_id, amount, period_start)
		SELECT id, level_id, 1000, '2026-10-01' FROM users WHERE id = ?`, owing); err != nil {
		t.Fatal(err)
	}
	code, body, _ := do(http.MethodGet, fmt.Sprintf("/admin/emails/member/%d", owing), "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	for _, want := range []string{"upozornění na dluh", "Předmět:", "srcdoc="} {
		if !strings.Contains(body, want) {
			t.Errorf("the member preview lacks %q", want)
		}
	}

	_, reminders, _ := do(http.MethodGet, "/admin/emails/reminders", "")
	if !strings.Contains(reminders, fmt.Sprintf(`/admin/emails/member/%d`, owing)) {
		t.Error("the picker does not link to the member preview")
	}
}
