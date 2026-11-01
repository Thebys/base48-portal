package handler

import (
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
		"Doplatek zvedáku<", // the approved note
		">výdaj<",           // an expense without a note
		"123456789/0800",    // members see donors' accounts
		"Zvedák a vybavení",
		`data-vs="202646"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Who was paid and what the bank said stay with the admins.
	for _, leak := range []string{"330981999", "1832792017", "Dodavatel zvedáků", "260908"} {
		if strings.Contains(page, leak) {
			t.Errorf("page shows %q of an expense", leak)
		}
	}
}

func TestRecentPaymentsShowWhatMoneyWentOn(t *testing.T) {
	e := newAccessEnv(t)
	member, _ := e.user("member", "accepted", nil)
	for _, query := range []string{
		`INSERT INTO projects (id, name) VALUES (10, 'Garáž')`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification)
		 VALUES (date('now'), '1000', 'fio', 'fee', 'FIO', '2400139123/2010', '4242')`,
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
	for _, want := range []string{"+1 000 Kč", "-9 693 Kč", "Doplatek za betonování", "Garáž", "-44 000 Kč", ">výdaj<", "4242",
		"přišlo 1 000 Kč, odešlo 53 693 Kč"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, leak := range []string{"3446840227", "3131267163", "260163", "2400139123"} {
		if strings.Contains(page, leak) {
			t.Errorf("page shows %q", leak)
		}
	}
}
