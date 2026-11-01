package migrations

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
)

// The cases 021 has to sort out are the ones found in production data.
func TestProjectPaymentsMoveToProjectID(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := migrate.Run(database, migrationsBefore(t, "021_")); err != nil {
		t.Fatalf("migrations up to 020: %v", err)
	}
	mustExec := func(query string, args ...interface{}) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	mustExec(`INSERT INTO users (id, email, level_id, payments_id, state) VALUES (1, 'm@base48.cz', 1, '31337', 'accepted')`)
	mustExec(`INSERT INTO projects (id, name) VALUES (10, 'Dary'), (11, 'Garáž'), (12, 'Smazaný')`)
	mustExec(`INSERT INTO project_vs (project_id, vs) VALUES (10, '1337'), (10, '31337'), (11, '202646'), (12, '777')`)
	mustExec(`DELETE FROM projects WHERE id = 12`) // project_vs row left behind, foreign keys were off

	pay := func(id int, userID, projectID interface{}, amount, vs string) {
		t.Helper()
		mustExec(`INSERT INTO payments (id, user_id, project_id, date, amount, kind, kind_id, local_account, remote_account, identification)
		          VALUES (?, ?, ?, '2026-05-01', ?, 'fio', ?, 'FIO', '1/0100', ?)`, id, userID, projectID, amount, id, vs)
	}
	pay(1, nil, nil, "1000", "1337")    // gift with the project's VS: joins Dary
	pay(2, nil, 11, "2000", "1337")     // assigned to the garage by hand: stays there
	pay(3, 1, nil, "200", "31337")      // member fee with a VS that is also a Dary alias: stays the member's
	pay(4, nil, nil, "-1560", "202646") // expense paid with the garage VS: joins the garage
	pay(5, nil, nil, "500", "777")      // VS of a deleted project: stays unassigned
	pay(6, nil, nil, "300", "")         // no VS: stays unassigned

	if err := migrate.Run(database, FS); err != nil {
		t.Fatalf("migrations 021+: %v", err)
	}
	want := map[int]string{1: "10", 2: "11", 3: "", 4: "11", 5: "", 6: ""}
	check := func(when string) {
		t.Helper()
		for id, project := range want {
			var got sql.NullString
			if err := database.QueryRow(`SELECT CAST(project_id AS TEXT) FROM payments WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got.String != project {
				t.Errorf("%s: payment %d project = %q, want %q", when, id, got.String, project)
			}
		}
	}
	check("after 021")

	// Run on its own again (it runs without a transaction): nothing moves.
	again, err := fs.ReadFile(FS, "021_project_payments_by_id.sql")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(string(again))
	check("second run")

	// Totals read project_id only: the member fee is not Dary's, the garage
	// keeps the gift assigned by hand and shows the expense as spent.
	totals, err := db.New(database).ListProjectTotals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]db.ListProjectTotalsRow{}
	for _, row := range totals {
		got[row.ProjectID] = row
	}
	if r := got[10]; r.Raised != 1000 || r.Spent != 0 {
		t.Errorf("Dary = %+v, want raised 1000, spent 0", r)
	}
	if r := got[11]; r.Raised != 2000 || r.Spent != 1560 {
		t.Errorf("garage = %+v, want raised 2000, spent 1560", r)
	}
}
