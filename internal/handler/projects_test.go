package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectVSBringsEarlierPaymentsAlong(t *testing.T) {
	q, database := testQueries(t)
	h := &Handler{queries: q}
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO users (id, email, level_id, payments_id, state) VALUES (1, 'm@base48.cz', 1, '4242', 'accepted')`)
	exec(`INSERT INTO projects (id, name) VALUES (10, 'Garáž')`)
	exec(`INSERT INTO project_vs (project_id, vs, note) VALUES (10, '202646', 'primary')`)
	for i, row := range []struct {
		userID interface{}
		vs     string
	}{{nil, "20264"}, {nil, "20264"}, {1, "20264"}, {nil, "999"}} {
		exec(`INSERT INTO payments (user_id, date, amount, kind, kind_id, local_account, remote_account, identification)
		      VALUES (?, '2026-05-01', '100', 'fio', ?, 'FIO', '1/0100', ?)`, row.userID, i, row.vs)
	}
	projectOf := func() string {
		t.Helper()
		var out []string
		rows, err := database.Query(`SELECT COALESCE(CAST(project_id AS TEXT), '-') FROM payments ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			rows.Scan(&s)
			out = append(out, s)
		}
		return strings.Join(out, ",")
	}
	call := func(handler http.HandlerFunc, method, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(method, "/api/admin/projects/vs", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s", method, body, rec.Code, rec.Body)
		}
	}

	// The typo alias pulls in the two unassigned payments, not the member's.
	call(h.AdminAddProjectVSHandler, http.MethodPost, `{"project_id":10,"vs":"20264","note":"překlep"}`)
	if got := projectOf(); got != "10,10,-,-" {
		t.Fatalf("after adding the VS: %s, want 10,10,-,-", got)
	}
	n, err := q.CountProjectPayments(context.Background(), sql.NullInt64{Int64: 10, Valid: true})
	if err != nil || n != 2 {
		t.Fatalf("project payments = %d (%v), want 2", n, err)
	}

	// Taking the alias away lets them go again.
	call(h.AdminRemoveProjectVSHandler, http.MethodDelete, `{"project_id":10,"vs":"20264"}`)
	if got := projectOf(); got != "-,-,-,-" {
		t.Fatalf("after removing the VS: %s, want -,-,-,-", got)
	}
}

func TestDeleteProjectKeepsPaymentsAttached(t *testing.T) {
	q, database := testQueries(t)
	h := &Handler{queries: q, db: database}
	for _, query := range []string{
		`INSERT INTO projects (id, name) VALUES (10, 'Garáž'), (11, 'Prázdný')`,
		`INSERT INTO project_vs (project_id, vs) VALUES (10, '202646'), (11, '555')`,
		`INSERT INTO payments (project_id, date, amount, kind, kind_id, local_account, remote_account, identification)
		 VALUES (10, '2026-05-17', '-1560', 'fio', '1', 'FIO', '1/3030', '202646')`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	del := func(id string) int {
		rec := httptest.NewRecorder()
		h.AdminDeleteProjectHandler(rec, httptest.NewRequest(http.MethodDelete, "/api/admin/projects", strings.NewReader(`{"project_id":`+id+`}`)))
		return rec.Code
	}

	if code := del("10"); code != http.StatusConflict {
		t.Fatalf("deleting a project with a payment = %d, want 409", code)
	}
	if code := del("11"); code != http.StatusOK {
		t.Fatalf("deleting an empty project = %d, want 200", code)
	}
	// Its symbol goes with it, so another project can take it.
	if _, err := q.GetProjectVSByVS(context.Background(), "555"); err != sql.ErrNoRows {
		t.Fatalf("symbol of the deleted project: err = %v, want sql.ErrNoRows", err)
	}
}

func TestProjectVSMustBeFreeDigits(t *testing.T) {
	q, database := testQueries(t)
	h := &Handler{queries: q, db: database}
	for _, query := range []string{
		`INSERT INTO users (id, email, level_id, payments_id, state) VALUES (1, 'm@base48.cz', 1, '4242', 'accepted')`,
		`INSERT INTO projects (id, name) VALUES (10, 'Dary')`,
		`INSERT INTO project_vs (project_id, vs) VALUES (10, '1337')`,
		`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification)
		 VALUES ('2026-05-01', '500', 'fio', '1', 'FIO', '1/0100', '202646')`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	create := func(body string) int {
		rec := httptest.NewRecorder()
		h.AdminCreateProjectHandler(rec, httptest.NewRequest(http.MethodPost, "/api/admin/projects", strings.NewReader(body)))
		return rec.Code
	}
	addVS := func(body string) int {
		rec := httptest.NewRecorder()
		h.AdminAddProjectVSHandler(rec, httptest.NewRequest(http.MethodPost, "/api/admin/projects/vs", strings.NewReader(body)))
		return rec.Code
	}

	for body, want := range map[string]int{
		`{"name":"Garáž","payments_id":"4242"}`:          http.StatusConflict,   // a member's VS
		`{"name":"Garáž","payments_id":"1337"}`:          http.StatusConflict,   // another project's
		`{"name":"Garáž","payments_id":"20x"}`:           http.StatusBadRequest, // not a VS
		`{"name":"Garáž","payments_id":"0123456789012"}`: http.StatusBadRequest,
	} {
		if got := create(body); got != want {
			t.Errorf("create %s = %d, want %d", body, got, want)
		}
	}
	if got := addVS(`{"project_id":10,"vs":"4242"}`); got != http.StatusConflict {
		t.Errorf("adding a member's VS = %d, want 409", got)
	}

	// Nothing half-made was left behind by the refusals.
	if n, _ := q.ListProjects(context.Background()); len(n) != 2 { // Dary + Energie 2024 from migration 005
		t.Fatalf("projects after refusals = %d, want 2", len(n))
	}

	// A good one is created with its symbol and picks up the earlier payment.
	if got := create(`{"name":"Garáž","payments_id":"202646"}`); got != http.StatusOK {
		t.Fatalf("create with a free VS = %d, want 200", got)
	}
	var project sql.NullInt64
	if err := database.QueryRow(`SELECT project_id FROM payments WHERE kind_id = '1'`).Scan(&project); err != nil || !project.Valid {
		t.Fatalf("earlier payment with the new VS: project %v (err %v), want assigned", project, err)
	}
}
