package migrations

import (
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/migrate"
)

// migrationsBefore is FS cut off before the migration with the given prefix,
// so a test can fill the database the way production looked before it.
func migrationsBefore(t *testing.T, prefix string) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() >= prefix {
			continue
		}
		data, err := fs.ReadFile(FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = &fstest.MapFile{Data: data}
	}
	return out
}

// rowsOf runs query and returns each row's columns joined with "|", so two
// schema snapshots can be compared as plain strings.
func rowsOf(t *testing.T, database *sql.DB, query string, args ...interface{}) []string {
	t.Helper()
	rows, err := database.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = v.String
		}
		out = append(out, strings.Join(parts, "|"))
	}
	return out
}

// usersSchema is everything about users the rebuild must keep: each column
// (name, type, NOT NULL, default, primary key) and each index with its
// definition, partial WHERE included.
func usersSchema(t *testing.T, database *sql.DB) string {
	t.Helper()
	columns := rowsOf(t, database, `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info('users') ORDER BY cid`)
	indexes := rowsOf(t, database, `SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'users' ORDER BY name`)
	return strings.Join(columns, "\n") + "\n--\n" + strings.Join(indexes, "\n")
}

func TestExternalStateRebuildKeepsUsersAndReferences(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// One connection, so the pragmas below and the ones in the migration are
	// all on the connection the assertions run on.
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	if err := migrate.Run(database, migrationsBefore(t, "018_")); err != nil {
		t.Fatalf("migrations up to 017: %v", err)
	}

	mustExec := func(query string, args ...interface{}) {
		t.Helper()
		if _, err := database.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	// Three users, then the newest one deleted: the counter must stay ahead of
	// it after the rebuild.
	mustExec(`INSERT INTO users (id, keycloak_id, email, username, level_id, payments_id, state, locale, keys_granted)
	          VALUES (1, 'kc-1', 'member@base48.cz', 'member', 1, '1001', 'accepted', 'en', '2025-01-02 03:04:05')`)
	mustExec(`INSERT INTO users (id, email, level_id, state) VALUES (2, 'applicant@base48.cz', 1, 'awaiting')`)
	mustExec(`INSERT INTO users (id, email, level_id, state) VALUES (3, 'gone@base48.cz', 1, 'rejected')`)
	mustExec(`DELETE FROM users WHERE id = 3`)

	mustExec(`INSERT INTO payments (user_id, date, amount, kind, kind_id, local_account, remote_account, identification)
	          VALUES (1, '2026-09-01', '1000', 'fio', 'tx-1', 'FIO', '123/0100', '1001')`)
	mustExec(`INSERT INTO fees (user_id, level_id, period_start, amount) VALUES (1, 1, '2026-09-01', '1000')`)
	mustExec(`INSERT INTO system_logs (subsystem, level, user_id, message) VALUES ('test', 'info', 2, 'hello')`)
	mustExec(`INSERT INTO reservations (resource_id, user_id, starts_at, ends_at)
	          VALUES ((SELECT id FROM resources WHERE slug = 'bay-1'), 1, '2026-10-01 08:00', '2026-10-01 12:00')`)

	schemaBefore := usersSchema(t, database)

	if err := migrate.Run(database, FS); err != nil {
		t.Fatalf("migrations 018+: %v", err)
	}

	// Rows survive with every column intact.
	var (
		count                                    int
		email, username, payments, state, locale string
		keycloakID, keysGranted                  sql.NullString
	)
	if err := database.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("users after rebuild = %d (err %v), want 2", count, err)
	}
	if err := database.QueryRow(`SELECT keycloak_id, email, username, payments_id, state, locale, CAST(keys_granted AS TEXT) FROM users WHERE id = 1`).
		Scan(&keycloakID, &email, &username, &payments, &state, &locale, &keysGranted); err != nil {
		t.Fatal(err)
	}
	if keycloakID.String != "kc-1" || email != "member@base48.cz" || username != "member" || payments != "1001" ||
		state != "accepted" || locale != "en" || keysGranted.String != "2025-01-02 03:04:05" {
		t.Errorf("user 1 after rebuild = %v %s %s %s %s %s %v", keycloakID, email, username, payments, state, locale, keysGranted)
	}

	// Same columns and the same indexes, defined the same way.
	if got := usersSchema(t, database); got != schemaBefore {
		t.Errorf("users schema changed by the rebuild:\n got:\n%s\n want:\n%s", got, schemaBefore)
	}

	// References from other tables still resolve, and are still enforced.
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("PRAGMA foreign_key_check reports violations after the rebuild")
	}
	rows.Close()
	var fk int
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("PRAGMA foreign_keys after the migration = %d (err %v), want 1", fk, err)
	}
	if _, err := database.Exec(`INSERT INTO fees (user_id, level_id, period_start, amount) VALUES (99, 1, '2026-10-01', '1')`); err == nil {
		t.Error("a fee for a missing user was accepted: fees no longer references users")
	}
	if _, err := database.Exec(`DELETE FROM users WHERE id = 1`); err == nil {
		t.Error("deleting a user with payments succeeded: payments no longer references users")
	}

	// The new state is allowed, nonsense still is not.
	mustExec(`UPDATE users SET state = 'external' WHERE id = 2`)
	if _, err := database.Exec(`UPDATE users SET state = 'guest' WHERE id = 2`); err == nil {
		t.Error("state 'guest' was accepted, the CHECK constraint is gone")
	}

	// A new user does not get the id of the deleted one.
	res, err := database.Exec(`INSERT INTO users (email, level_id) VALUES ('new@base48.cz', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 4 {
		t.Errorf("new user id = %d, want 4 (id 3 belonged to a deleted user)", id)
	}

	// access_grants exists and points at users.
	mustExec(`INSERT INTO access_grants (user_id, scope, granted_by) VALUES (2, 'workshop', 1)`)
	if _, err := database.Exec(`INSERT INTO access_grants (user_id, scope) VALUES (2, 'workshop')`); err == nil {
		t.Error("a second identical grant was accepted")
	}
	if _, err := database.Exec(`INSERT INTO access_grants (user_id, scope) VALUES (99, 'workshop')`); err == nil {
		t.Error("a grant for a missing user was accepted")
	}
}

func TestExternalStateOnEmptyDatabase(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := migrate.Run(database, FS); err != nil {
		t.Fatalf("all migrations on an empty database: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO users (email, level_id, state) VALUES ('x@base48.cz', 1, 'external')`); err != nil {
		t.Errorf("insert external user: %v", err)
	}
}
