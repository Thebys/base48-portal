package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestFundraisingShowsWhatWasSpentAndOnWhat(t *testing.T) {
	e := newAccessEnv(t)
	member, _ := e.user("member", "accepted", nil)

	for _, query := range []string{
		`INSERT INTO projects (id, name, description) VALUES (10, 'Garáž', 'Zvedák a vybavení')`,
		`INSERT INTO project_vs (project_id, vs, note) VALUES (10, '202646', 'primary')`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, project_id)
		 VALUES ('2026-05-15', '50000', 'fio', 'gift', 'FIO', '123456789/0800', '202646', 10)`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, project_id, raw_data, public_note)
		 VALUES ('2026-10-21', '-22977.9', 'fio', 'lift', 'FIO', '330981999/0300', '260908', 10,
		         '{"column10":"Dodavatel zvedáků","column16":"Doplatek zvedáku 260908"}', 'Doplatek zvedáku')`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, project_id, public_note)
		 VALUES ('2026-05-17', '-1560', 'fio', 'shelves', 'FIO', '1832792017/3030', '202646', 10, NULL)`,
	} {
		if _, err := e.database.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	rec := e.do(member, http.MethodGet, "/projects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /projects = %d", rec.Code)
	}
	page := rec.Body.String()
	nbsp := " "
	for _, want := range []string{
		"25" + nbsp + "462,10 Kč",          // left: 50 000 - 22 977,90 - 1 560
		"vybráno 50" + nbsp + "000 Kč",     // raised
		"utraceno 24" + nbsp + "537,90 Kč", // spent
		"-22" + nbsp + "977,90 Kč",
		"Doplatek zvedáku", // the approved note
		">výdaj<",          // an expense without a note
		"Zvedák a vybavení",
		`data-vs="202646"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Who was paid and what the bank said stay with the admins.
	for _, leak := range []string{"123456789/0800", "330981999", "1832792017", "Dodavatel zvedáků", "260908", "Smazat projekt", "Přidat VS"} {
		if strings.Contains(page, leak) {
			t.Errorf("page shows %q", leak)
		}
	}

	// The admin page is the same page with the controls and what members do
	// not see of an expense: whom it was paid to, and the staff comment.
	if _, err := e.database.Exec(`UPDATE payments SET staff_comment = 'faktura v šanonu' WHERE kind_id = 'lift'`); err != nil {
		t.Fatal(err)
	}
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	if rec := e.do(member, http.MethodGet, "/admin/projects", ""); rec.Code == http.StatusOK {
		t.Fatal("a member got the admin projects page")
	}
	rec = e.do(admin, http.MethodGet, "/admin/projects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/projects as admin = %d", rec.Code)
	}
	adminPage := rec.Body.String()
	for _, want := range []string{"Přidat VS", "Projekt s platbami nejde smazat", "123456789/0800", "330981999/0300", "faktura v šanonu", "25" + nbsp + "462,10 Kč"} {
		if !strings.Contains(adminPage, want) {
			t.Errorf("admin page lacks %q", want)
		}
	}
}

func TestRecentPaymentsShowWhatMoneyWentOn(t *testing.T) {
	e := newAccessEnv(t)
	member, memberID := e.user("member", "accepted", nil)
	_, otherID := e.user("other", "accepted", nil)
	var ownVS, otherVS string
	e.database.QueryRow(`SELECT payments_id FROM users WHERE id = ?`, memberID).Scan(&ownVS)
	e.database.QueryRow(`SELECT payments_id FROM users WHERE id = ?`, otherID).Scan(&otherVS)

	for _, query := range []string{
		`INSERT INTO projects (id, name) VALUES (10, 'Garáž')`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, user_id)
		 VALUES (date('now'), '1000', 'fio', 'own', 'FIO', '1111111111/2010', '` + ownVS + `', ` + fmt.Sprint(memberID) + `)`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, user_id)
		 VALUES (date('now'), '1200', 'fio', 'other', 'FIO', '2400139123/2010', '` + otherVS + `', ` + fmt.Sprint(otherID) + `)`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, project_id)
		 VALUES (date('now'), '500', 'fio', 'gift', 'FIO', '123456789/0800', '202646', 10)`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification, project_id, public_note)
		 VALUES (date('now'), '-9693', 'fio', 'concrete', 'FIO', '3446840227/0100', '260163', 10, 'Doplatek za betonování')`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification)
		 VALUES (date('now'), '-44000', 'fio', 'rent', 'FIO', '3131267163/0800', '3872751')`,
	} {
		if _, err := e.database.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	page := e.do(member, http.MethodGet, "/payments/recent", "").Body.String()
	for _, want := range []string{"+1\u00a0000 Kč", "-9\u00a0693 Kč", "Doplatek za betonování", "Garáž", "-44\u00a0000 Kč", ">výdaj<",
		"vaše platba", "VS " + ownVS, "členský příspěvek", "přišlo 2\u00a0700 Kč, odešlo 53\u00a0693 Kč"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Private by default: nobody else's VS or account, nor whom expenses went to.
	for _, leak := range []string{otherVS, "2400139123", "123456789", "3446840227", "3131267163", "260163", "202646"} {
		if strings.Contains(page, leak) {
			t.Errorf("page shows %q", leak)
		}
	}
}
