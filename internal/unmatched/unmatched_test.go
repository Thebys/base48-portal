package unmatched

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

func TestListSortsLikeTheSync(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	// User 5 pays with VS 1005. The old report read a VS as a user id, so it
	// took VS "5" for user 5 and VS "1005" for a missing user.
	exec(`INSERT INTO users (id, email, level_id, payments_id, state) VALUES (5, 'm@base48.cz', 1, '1005', 'accepted')`)
	pay := func(kindID, amount, vs string) {
		exec(`INSERT INTO payments (date, amount, kind, kind_id, local_account, remote_account, identification)
		      VALUES ('2026-10-01', ?, 'fio', ?, 'FIO', '1/0100', ?)`, amount, kindID, vs)
	}
	pay("empty", "300", "")
	pay("unknown", "400", "5")
	pay("member", "500", "1005")
	pay("interest", "2.50", "")
	pay("outgoing", "-44000", "3872751")

	got, err := List(context.Background(), db.New(database))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Category{"empty": EmptyVS, "unknown": UserNotFound, "member": SyncBug}
	if len(got) != len(want) {
		t.Fatalf("listed %d payments, want %d: %+v", len(got), len(want), got)
	}
	for _, p := range got {
		if want[p.KindID] != p.Category {
			t.Errorf("%s: category %s, want %s", p.KindID, p.Category, want[p.KindID])
		}
	}
}
