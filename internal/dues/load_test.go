package dues

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

func TestLoad(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatal(err)
	}
	q := db.New(database)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO levels (id, name, amount, active) VALUES (50, 'std', 1000, 1)`)
	exec(`INSERT INTO users (id, email, level_id, level_actual_amount, payments_id, state) VALUES (1, 'a@b.cz', 50, '1200', '11', 'accepted')`)
	exec(`INSERT INTO users (id, email, level_id, payments_id, state) VALUES (2, 'new@b.cz', 50, '12', 'accepted')`)
	exec(`INSERT INTO users (id, email, level_id, payments_id, state) VALUES (3, 'ahead@b.cz', 50, '13', 'accepted')`)
	for _, m := range []string{"2026-10-01", "2026-11-01"} {
		exec(`INSERT INTO fees (user_id, level_id, amount, period_start) VALUES (1, 50, 1200, ?)`, m)
	}
	// An admin entered December ahead for the third member.
	for _, m := range []string{"2026-11-01", "2026-12-01"} {
		exec(`INSERT INTO fees (user_id, level_id, amount, period_start) VALUES (3, 50, 1000, ?)`, m)
	}
	exec(`INSERT INTO payments (user_id, amount, identification, date, kind, kind_id, local_account, remote_account)
	      VALUES (1, 3000, '11', '2026-10-05', 'manual', 'p1', '', '')`)

	// No run recorded: the latest month charged, not December entered ahead.
	run, err := RunMonth(ctx, q, now)
	if err != nil || run != nov {
		t.Fatalf("run month %v, %v; want 2026-11", run, err)
	}

	users, err := q.ListAcceptedUsersForFees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	all, err := LoadAll(ctx, q, users, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]Standing{
		1: {Fee: 1200, Balance: 600, Anchor: nov},
		2: {Fee: 1000, Balance: 0, Anchor: nov}, // joined after the run: not due for November
		3: {Fee: 1000, Balance: -2000, Anchor: nov.Add(1)},
	}
	for id, w := range want {
		if all[id] != w {
			t.Errorf("member %d: %+v, want %+v", id, all[id], w)
		}
		user, err := q.GetUserByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		one, err := Load(ctx, q, &user, "1000", now)
		if err != nil || one != w {
			t.Errorf("Load member %d: %+v, %v; want %+v", id, one, err, w)
		}
	}

	// Once the run records its month, that is what counts.
	if _, err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: SettingFeesMonth, Value: "2026-12"}); err != nil {
		t.Fatal(err)
	}
	if run, err := RunMonth(ctx, q, now); err != nil || run != nov.Add(1) {
		t.Errorf("recorded run month: %v, %v", run, err)
	}
}
