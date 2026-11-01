package migrations

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/migrate"
)

func TestOutgoingPaymentsGetANoteFromTheBank(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrate.Run(database, migrationsBefore(t, "022_")); err != nil {
		t.Fatalf("migrations up to 021: %v", err)
	}

	pay := func(id int, amount, raw string) {
		t.Helper()
		if _, err := database.Exec(`INSERT INTO payments (id, date, amount, kind, kind_id, local_account, remote_account, identification, raw_data)
		      VALUES (?, '2025-11-13', ?, 'fio', ?, 'FIO', '1/0800', '', ?)`, id, amount, id, raw); err != nil {
			t.Fatal(err)
		}
	}
	// FIO's own shape, from the old portal's import: comment wins.
	pay(1, "-51097", `{"column16":{"value":"energie"},"column25":{"value":"energie doplatek"}}`)
	// The same, only a message.
	pay(2, "-44000", `{"column16":{"value":" najemne "},"column25":null}`)
	// This portal's shape.
	pay(3, "-193.6", `{"column16":"","column25":"Forpsi VPS"}`)
	// Nothing to go by, or no JSON at all.
	pay(4, "-1702", `{"column16":{"value":""}}`)
	pay(5, "-200", `not json`)
	// Incoming payments get no note.
	pay(6, "1000", `{"column16":{"value":"clensky prispevek"}}`)

	if err := migrate.Run(database, FS); err != nil {
		t.Fatalf("migrations 022+: %v", err)
	}
	want := map[int]string{1: "energie doplatek", 2: "najemne", 3: "Forpsi VPS", 4: "", 5: "", 6: ""}
	for id, note := range want {
		var got sql.NullString
		if err := database.QueryRow(`SELECT public_note FROM payments WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got.String != note {
			t.Errorf("payment %d note = %q, want %q", id, got.String, note)
		}
	}
}
