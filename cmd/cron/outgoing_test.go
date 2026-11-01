package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/fio"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

func outgoingTestDB(t *testing.T) (*db.Queries, *sql.DB) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users (id, email, level_id, payments_id, state) VALUES (1, 'm@base48.cz', 1, '48', 'accepted')`,
		`INSERT INTO projects (id, name) VALUES (10, 'Garáž')`,
		`INSERT INTO project_vs (project_id, vs) VALUES (10, '202646')`,
	} {
		if _, err := database.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	return db.New(database), database
}

func TestOutgoingPaymentIsRecordedOnceAndNeverAMembers(t *testing.T) {
	q, database := outgoingTestDB(t)
	ctx := context.Background()
	record := func(tx fio.Transaction) bool {
		t.Helper()
		ok, err := recordOutgoing(ctx, q, tx, "2003436319/2010")
		if err != nil {
			t.Fatalf("record %d: %v", tx.ID, err)
		}
		return ok
	}

	shelves := fio.Transaction{ID: 1, Date: "2026-05-17+0200", Amount: -1560, AccountNumber: "1832792017", BankCode: "3030",
		VariableSymbol: "202646", Message: "3x regál do garáže", Comment: "3x regál do garáže"}
	invoice := fio.Transaction{ID: 2, Date: "2026-07-15+0200", Amount: -1829.92, AccountNumber: "4793250277", BankCode: "0100",
		VariableSymbol: "48", Message: "CISLO FAKTURY: 48"} // the VS happens to be the member's
	concrete := fio.Transaction{ID: 3, Date: "2026-09-04+0200", Amount: -9693, VariableSymbol: "260163",
		Message: "DOPRAVA BETONU", Comment: "Doplatek za betonování"}

	for _, tx := range []fio.Transaction{shelves, invoice, concrete} {
		if !record(tx) {
			t.Fatalf("payment %d not recorded the first time", tx.ID)
		}
	}

	// The expense paid with the garage VS is the garage's, the invoice is
	// nobody's: no member pays for it.
	var balance int64
	if err := database.QueryRow(`SELECT COALESCE(SUM(CAST(amount AS REAL)), 0) FROM payments WHERE user_id = 1`).Scan(&balance); err != nil || balance != 0 {
		t.Fatalf("member's payments sum to %d (err %v), want 0", balance, err)
	}
	rows := map[string]string{}
	r, err := database.Query(`SELECT kind_id, COALESCE(CAST(project_id AS TEXT), '-') || '|' || date || '|' || COALESCE(public_note, '') || '|' || remote_account FROM payments`)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var id, v string
		r.Scan(&id, &v)
		rows[id] = v
	}
	r.Close()
	want := map[string]string{
		"1": "10|2026-05-17|3x regál do garáže|1832792017/3030",
		"2": "-|2026-07-15|CISLO FAKTURY: 48|4793250277/0100",
		"3": "-|2026-09-04|Doplatek za betonování|",
	}
	for id, w := range want {
		if rows[id] != w {
			t.Errorf("payment %s = %q, want %q", id, rows[id], w)
		}
	}

	// An admin books the concrete to the garage and rewrites the note; the
	// next sync sees the same payment and must leave both alone.
	var paymentID int64
	database.QueryRow(`SELECT id FROM payments WHERE kind_id = '3'`).Scan(&paymentID)
	if _, err := q.AssignOutgoingPayment(ctx, db.AssignOutgoingPaymentParams{
		ID:         paymentID,
		ProjectID:  sql.NullInt64{Int64: 10, Valid: true},
		PublicNote: sql.NullString{String: "Beton pod zvedák", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if record(concrete) {
		t.Fatal("an outgoing payment was recorded twice")
	}
	var project, note string
	database.QueryRow(`SELECT CAST(project_id AS TEXT), public_note FROM payments WHERE id = ?`, paymentID).Scan(&project, &note)
	if project != "10" || note != "Beton pod zvedák" {
		t.Fatalf("after the next sync: project %s, note %q; want 10, the admin's note", project, note)
	}
}

// statementOf is a FIO statement of the given account with the given
// transactions, in FIO's JSON format.
func statementOf(iban, accountID string, transactions ...string) string {
	return `{"accountStatement":{"info":{"accountId":"` + accountID + `","bankId":"2010","iban":"` + iban + `",
	  "dateStart":"2026-02-12+0100","dateEnd":"2026-11-01+0100"},
	  "transactionList":{"transaction":[` + strings.Join(transactions, ",") + `]}}}`
}

func TestBackfillRecordsOutgoingOfThePaymentAccountOnly(t *testing.T) {
	q, database := outgoingTestDB(t)
	ctx := context.Background()
	if _, err := bank.Record(ctx, q, bank.Account{
		IBAN: "CZ6620100000002003436319", BIC: "FIOBCZPPXXX", Number: "2003436319", BankCode: "2010",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	lift := `{"column22":{"value":27900000001},"column0":{"value":"2026-07-24+0200"},"column1":{"value":-22977.9},
	          "column2":{"value":"330981999"},"column3":{"value":"0300"},"column5":{"value":"260020"},
	          "column16":{"value":"PLATBA ZA FAKTURU C. 260020"}}`
	fee := `{"column22":{"value":27900000002},"column0":{"value":"2026-07-25+0200"},"column1":{"value":1000.0},
	         "column5":{"value":"48"}}`
	run := func(statement string) int {
		return runFIOBackfill(ctx, &config.Config{}, q, []string{"-"}, strings.NewReader(statement))
	}
	count := func(where string) (n int) {
		database.QueryRow(`SELECT COUNT(*) FROM payments WHERE ` + where).Scan(&n)
		return n
	}

	// The old transparent account's statement is refused as a whole.
	if code := run(statementOf("CZ0520100000002900086515", "2900086515", lift)); code == 0 {
		t.Fatal("backfill took another account's statement")
	}
	if n := count("1 = 1"); n != 0 {
		t.Fatalf("%d payments after a refused statement, want 0", n)
	}

	// Of the payment account's: the expense goes in, the fee is only reported.
	if code := run(statementOf("CZ6620100000002003436319", "2003436319", lift, fee)); code != 0 {
		t.Fatalf("backfill = %d, want 0", code)
	}
	if count(`kind_id = '27900000001' AND local_account = '2003436319/2010'`) != 1 || count(`kind_id = '27900000002'`) != 0 {
		t.Fatal("want the outgoing payment recorded and the incoming one left alone")
	}
	// Run again: nothing new.
	if code := run(statementOf("CZ6620100000002003436319", "2003436319", lift, fee)); code != 0 || count("1 = 1") != 1 {
		t.Fatalf("second backfill = %d with %d payments, want 0 with 1", code, count("1 = 1"))
	}
}
