package main

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

// A fee amount stored before the profile checked it ("NaN" from the pentest
// of 2026-10-23) must not become a fee SQLite sums as 0.
func TestFeesFallBackToLevelAmountForInvalidCustomAmount(t *testing.T) {
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatal(err)
	}

	custom := map[string]string{
		"nan@base48.cz":    "NaN",
		"inf@base48.cz":    "+Inf",
		"junk@base48.cz":   "1000abc",
		"unset@base48.cz":  "0",
		"custom@base48.cz": "1500",
	}
	for email, amount := range custom {
		if _, err := database.Exec(
			`INSERT INTO users (email, level_id, level_actual_amount, state) VALUES (?, 3, ?, 'accepted')`,
			email, amount,
		); err != nil {
			t.Fatal(err)
		}
	}

	if code := runFees(context.Background(), &config.Config{}, db.New(database)); code != 0 {
		t.Fatalf("runFees exit code %d", code)
	}

	want := map[string]string{
		"nan@base48.cz":    "1000",
		"inf@base48.cz":    "1000",
		"junk@base48.cz":   "1000",
		"unset@base48.cz":  "1000",
		"custom@base48.cz": "1500",
	}
	for email, amount := range want {
		var got string
		err := database.QueryRow(
			`SELECT f.amount FROM fees f JOIN users u ON u.id = f.user_id WHERE u.email = ?`, email,
		).Scan(&got)
		if err != nil {
			t.Errorf("%s: no fee: %v", email, err)
			continue
		}
		if got != amount {
			t.Errorf("%s (level_actual_amount %q): fee %q, want %q", email, custom[email], got, amount)
		}
	}
}
