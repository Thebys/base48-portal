package bank

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

// base48 is the account as FIO describes it in a statement header.
var base48 = Account{
	IBAN:     "CZ6620100000002003436319",
	BIC:      "FIOBCZPPXXX",
	Number:   "2003436319",
	BankCode: "2010",
}

func testQueries(t *testing.T) (*db.Queries, *sql.DB) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return db.New(database), database
}

func TestValidate(t *testing.T) {
	with := func(change func(*Account)) Account {
		a := base48
		change(&a)
		return a
	}
	tests := []struct {
		name    string
		acc     Account
		wantErr string
	}{
		{"base48", base48, ""},
		{"prefixed account", Account{IBAN: "CZ6508000000192000145399", Number: "19-2000145399", BankCode: "0800"}, ""},
		{"no BIC", with(func(a *Account) { a.BIC = "" }), ""},
		{"checksum typo", with(func(a *Account) { a.IBAN = "CZ6620100000002003436318" }), "kontrolní součet"},
		{"number from another account", with(func(a *Account) { a.Number = "2800691518" }), "číslo účtu"},
		{"bank code mismatch", with(func(a *Account) { a.BankCode = "0800" }), "kód banky"},
		{"prefix the IBAN does not have", with(func(a *Account) { a.Number = "19-2003436319" }), "číslo účtu"},
		{"not Czech", with(func(a *Account) { a.IBAN = "SK3112000000198742637541" }), "český IBAN"},
		{"empty", Account{}, "český IBAN"},
		{"bad BIC", with(func(a *Account) { a.BIC = "FIO" }), "BIC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.acc.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.wantErr)
			}
		})
	}
}

func TestFormats(t *testing.T) {
	if got := base48.Domestic(); got != "2003436319/2010" {
		t.Errorf("Domestic() = %q", got)
	}
	if got := base48.PrettyIBAN(); got != "CZ66 2010 0000 0020 0343 6319" {
		t.Errorf("PrettyIBAN() = %q", got)
	}
	prefixed := Account{Number: "19-2000145399", BankCode: "0800"}
	if got := prefixed.Domestic(); got != "19-2000145399/0800" {
		t.Errorf("Domestic() with prefix = %q", got)
	}
}

func TestStale(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	a := base48
	a.CheckedAt = now.Add(-StaleAfter + time.Minute)
	if a.Stale(now) {
		t.Error("confirmed within the window, should not be stale")
	}
	a.CheckedAt = now.Add(-StaleAfter - time.Minute)
	if !a.Stale(now) {
		t.Error("unconfirmed for over a day, should be stale")
	}
}

func fioSyncLogs(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query(`SELECT level || ': ' || message FROM system_logs WHERE subsystem = 'fio_sync' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var logs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		logs = append(logs, s)
	}
	return logs
}

func TestRecordAndLoad(t *testing.T) {
	q, database := testQueries(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 4, 9, 51, 0, 0, time.UTC)

	if _, ok := Load(ctx, q); ok {
		t.Fatal("nothing recorded yet, Load should report unknown")
	}

	// FIO formats are not ours to trust: spaces and lower case are tidied up.
	messy := base48
	messy.IBAN = " cz66 2010 0000 0020 0343 6319 "
	if _, err := Record(ctx, q, messy, t0); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, ok := Load(ctx, q)
	if !ok {
		t.Fatal("Load after Record should know the account")
	}
	if got.IBAN != base48.IBAN || got.Domestic() != "2003436319/2010" || !got.CheckedAt.Equal(t0) {
		t.Errorf("loaded %+v", got)
	}
	if logs := fioSyncLogs(t, database); len(logs) != 1 || !strings.HasPrefix(logs[0], "info: ") {
		t.Fatalf("first record should log once as info, got %q", logs)
	}

	// The same account on the next sync only moves the confirmation time.
	t1 := t0.Add(2 * time.Minute)
	if _, err := Record(ctx, q, base48, t1); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(ctx, q); !got.CheckedAt.Equal(t1) {
		t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, t1)
	}
	if logs := fioSyncLogs(t, database); len(logs) != 1 {
		t.Fatalf("an unchanged account must not log again, got %q", logs)
	}

	// A statement that contradicts itself is refused and the known account stays.
	broken := base48
	broken.Number = "2800691518"
	if _, err := Record(ctx, q, broken, t1.Add(time.Minute)); err == nil {
		t.Fatal("Record should refuse an account whose number is not in its IBAN")
	}
	if got, _ := Load(ctx, q); got.Number != "2003436319" {
		t.Errorf("a refused record replaced the account: %+v", got)
	}

	// A new token reading another account: switched, and the change is logged.
	other := Account{IBAN: "CZ6508000000192000145399", BIC: "GIBACZPX", Number: "19-2000145399", BankCode: "0800"}
	if _, err := Record(ctx, q, other, t1.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(ctx, q); got.Domestic() != "19-2000145399/0800" {
		t.Errorf("after a token change, Load = %+v", got)
	}
	logs := fioSyncLogs(t, database)
	if len(logs) != 2 || !strings.HasPrefix(logs[1], "warning: ") ||
		!strings.Contains(logs[1], "2003436319/2010 → 19-2000145399/0800") {
		t.Fatalf("an account change should log a warning naming both accounts, got %q", logs)
	}
}

func TestLoadIgnoresUnusableValue(t *testing.T) {
	q, _ := testQueries(t)
	ctx := context.Background()
	for _, value := range []string{"not json", `{"iban":"CZ6620100000002003436318","number":"2003436319","bank_code":"2010"}`} {
		if _, err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settingKey, Value: value}); err != nil {
			t.Fatal(err)
		}
		if _, ok := Load(ctx, q); ok {
			t.Errorf("Load accepted %q", value)
		}
	}
}
