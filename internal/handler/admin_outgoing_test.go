package handler

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

func TestAdminBooksOutgoingPaymentsToProjects(t *testing.T) {
	e := newAccessEnv(t)
	admin, _ := e.user("admin", "accepted", []string{"memberportal_admin"})
	member, _ := e.user("member", "accepted", nil)

	for _, query := range []string{
		`INSERT INTO projects (id, name) VALUES (10, 'Garáž')`,
		`INSERT INTO payments (id, date, amount, kind, kind_id, local_account, remote_account, identification, raw_data, public_note)
		 VALUES (1, '2026-09-04', '-9693', 'fio', '1', 'FIO', '3446840227/0100', '260163',
		         '{"column10":"Betonárna <s.r.o.>","column16":"DOPRAVA BETONU","column25":"Doplatek za betonování"}', 'Doplatek za betonování')`,
		`INSERT INTO payments (id, date, amount, kind, kind_id, local_account, remote_account, identification)
		 VALUES (2, '2026-09-05', '1000', 'fio', '2', 'FIO', '1/0100', '1337')`,
	} {
		if _, err := e.database.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	if rec := e.do(member, http.MethodGet, "/admin/payments/outgoing?year=2026", ""); rec.Code == http.StatusOK {
		t.Fatal("a member got the admin page")
	}
	rec := e.do(admin, http.MethodGet, "/admin/payments/outgoing?year=2026", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin page = %d: %s", rec.Code, rec.Body)
	}
	page := rec.Body.String()
	for _, want := range []string{"Betonárna &lt;s.r.o.&gt;", "DOPRAVA BETONU", `value="Doplatek za betonování"`, "9 693 Kč"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, ">1 000 Kč") {
		t.Error("the incoming payment is listed among outgoing ones")
	}

	save := func(body string) int {
		return e.do(admin, http.MethodPost, "/api/admin/payments/outgoing", body).Code
	}
	if code := save(`{"payment_id":1,"project_id":10,"public_note":"Beton pod zvedák"}`); code != http.StatusOK {
		t.Fatalf("saving = %d", code)
	}
	var project sql.NullInt64
	var note sql.NullString
	e.database.QueryRow(`SELECT project_id, public_note FROM payments WHERE id = 1`).Scan(&project, &note)
	if project.Int64 != 10 || note.String != "Beton pod zvedák" {
		t.Fatalf("after saving: project %v, note %q", project, note.String)
	}

	if code := save(`{"payment_id":2,"project_id":10,"public_note":"x"}`); code != http.StatusBadRequest {
		t.Errorf("assigning an incoming payment here = %d, want 400", code)
	}
	if code := save(`{"payment_id":1,"project_id":99,"public_note":""}`); code != http.StatusNotFound {
		t.Errorf("assigning to a missing project = %d, want 404", code)
	}
	// Back to no project and no note.
	if code := save(`{"payment_id":1,"project_id":0,"public_note":"  "}`); code != http.StatusOK {
		t.Fatalf("clearing = %d", code)
	}
	e.database.QueryRow(`SELECT project_id, public_note FROM payments WHERE id = 1`).Scan(&project, &note)
	if project.Valid || note.Valid {
		t.Fatalf("after clearing: project %v, note %v; want both NULL", project, note)
	}
}

func TestBankTextsReadsBothRawShapes(t *testing.T) {
	for raw, want := range map[string][3]string{
		`{"column10":{"value":"Pronajímatel"},"column16":{"value":"najemne"},"column25":null}`: {"Pronajímatel", "najemne", ""},
		`{"column10":"","column16":"","column25":"Forpsi VPS"}`:                                {"", "", "Forpsi VPS"},
		`not json`: {"", "", ""},
	} {
		name, message, comment := bankTexts(sql.NullString{String: raw, Valid: true})
		if got := [3]string{name, message, comment}; got != want {
			t.Errorf("bankTexts(%s) = %q, want %q", raw, got, want)
		}
	}
}
