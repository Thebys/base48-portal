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
