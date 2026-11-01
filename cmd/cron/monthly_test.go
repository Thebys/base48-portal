package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

func TestMonthlyDue(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 12, d, 0, 1, 0, 0, time.Local) }
	for _, tc := range []struct {
		name                    string
		now                     time.Time
		fees, reminders         string
		wantFees, wantReminders bool
		wantMissed              bool
	}{
		{"the 1st", day(1), "2026-11", "2026-11", true, true, false},
		{"restart on the 1st after the run", day(1), "2026-12", "2026-12", false, false, false},
		{"reminders failed, fees done", day(1), "2026-12", "2026-11", false, true, false},
		{"down on the 1st, back on the 3rd", day(3), "2026-11", "2026-11", true, true, false},
		{"back on the 7th", day(7), "2026-11", "2026-11", true, true, false},
		{"back on the 8th: too late", day(8), "2026-11", "2026-11", false, false, true},
		{"reminders not made up after the first week", day(8), "2026-12", "2026-11", false, false, false},
		{"a whole month skipped", day(2), "2026-10", "2026-10", false, false, true},
		{"never recorded, the 1st", day(1), "", "", true, true, false},
		{"never recorded, the 2nd", day(2), "", "", false, false, false},
		{"January after December", time.Date(2027, 1, 1, 0, 1, 0, 0, time.Local), "2026-12", "2026-12", true, true, false},
	} {
		plan := monthlyDue(tc.now, tc.fees, tc.reminders)
		if plan.fees != tc.wantFees || plan.reminders != tc.wantReminders || (plan.missed != "") != tc.wantMissed {
			t.Errorf("%s: fees %t reminders %t missed %q; want %t %t %t",
				tc.name, plan.fees, plan.reminders, plan.missed, tc.wantFees, tc.wantReminders, tc.wantMissed)
		}
	}
}

func TestMonthStartUTC(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	for now, want := range map[time.Time]string{
		time.Date(2026, 12, 1, 0, 1, 0, 0, prague):  "2026-11-30 23:00:00",
		time.Date(2026, 7, 15, 12, 0, 0, 0, prague): "2026-06-30 22:00:00",
	} {
		if got := monthStartUTC(now); got != want {
			t.Errorf("monthStartUTC(%v) = %s, want %s", now, got, want)
		}
	}
}

// reminderDB is a database with a known payment account and members who owe
// the given number of 1000 Kč fees.
func reminderDB(t *testing.T, owed map[string]int) (*db.Queries, *sql.DB, *config.Config) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := migrate.Run(database, migrations.FS); err != nil {
		t.Fatal(err)
	}
	q := db.New(database)
	if _, err := bank.Record(context.Background(), q, bank.Account{
		IBAN: "CZ6620100000002003436319", BIC: "FIOBCZPPXXX", Number: "2003436319", BankCode: "2010",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var levelID int64
	if err := database.QueryRow(`INSERT INTO levels (name, amount, active) VALUES ('test', 1000, 1) RETURNING id`).Scan(&levelID); err != nil {
		t.Fatal(err)
	}
	for name, months := range owed {
		var id int64
		if err := database.QueryRow(
			`INSERT INTO users (email, username, level_id, payments_id, state) VALUES (?, ?, ?, ?, 'accepted') RETURNING id`,
			name+"@base48.cz", name, levelID, fmt.Sprintf("9%03d", len(name)*7+months),
		).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for m := 1; m <= months; m++ {
			if _, err := database.Exec(`INSERT INTO fees (user_id, level_id, amount, period_start) VALUES (?, ?, 1000, ?)`,
				id, levelID, fmt.Sprintf("2026-%02d-01", m)); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := &config.Config{EmailEnabled: true, WebRoot: "../../web", BaseURL: "https://portal.base48.cz"}
	return q, database, cfg
}

func outboxCount(t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM email_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A member gets one membership reminder a calendar month, however often the
// run goes. The 30-day window it replaces skipped every March, and December
// whenever the tick was a few seconds early.
func TestMembershipRemindersOncePerMonth(t *testing.T) {
	q, database, cfg := reminderDB(t, map[string]int{"one": 1, "two": 2, "none": 0})
	ctx := context.Background()
	december := time.Now()

	run, err := queueMembershipReminders(ctx, cfg, q, december, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if run.Queued != 2 || run.NoDebt != 1 {
		t.Fatalf("first run: %+v, want 2 queued and 1 without debt", run)
	}
	if month, _ := monthOf(ctx, q, settingRemindersMonth); month != december.Format("2006-01") {
		t.Errorf("reminders month recorded as %q", month)
	}

	again, err := queueMembershipReminders(ctx, cfg, q, december, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if again.Queued != 0 || again.Skipped != 2 || outboxCount(t, database) != 2 {
		t.Errorf("second run in the same month: %+v, %d in the outbox", again, outboxCount(t, database))
	}

	// Last month's reminders, sent exactly a month ago, do not block this
	// month's run.
	if _, err := database.Exec(`UPDATE email_outbox SET status = 'sent', created_at = datetime(created_at, '-1 month')`); err != nil {
		t.Fatal(err)
	}
	next, err := queueMembershipReminders(ctx, cfg, q, december, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if next.Queued != 2 {
		t.Errorf("a month later: %+v, want both reminded again", next)
	}

	// They leave at 10:00 local, about three days later.
	var due string
	if err := database.QueryRow(`SELECT MAX(next_retry_at) FROM email_outbox`).Scan(&due); err != nil {
		t.Fatal(err)
	}
	dueAt, err := time.Parse("2006-01-02 15:04:05", due[:19])
	if err != nil {
		t.Fatal(err)
	}
	if local := dueAt.In(time.Local); local.Hour() != reminderHour || local.Minute() != 0 {
		t.Errorf("due %s UTC, which is %s local; want 10:00", due, local.Format("15:04"))
	}
}

func TestMembershipRemindersDryRunQueuesNothing(t *testing.T) {
	q, database, cfg := reminderDB(t, map[string]int{"two": 2})
	var out strings.Builder
	run, err := queueMembershipReminders(context.Background(), cfg, q, time.Now(), true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if run.Queued != 1 || !strings.Contains(out.String(), "debt_warning") || !strings.Contains(out.String(), "two@base48.cz") {
		t.Errorf("dry run: %+v, output %q", run, out.String())
	}
	if outboxCount(t, database) != 0 {
		t.Error("the dry run queued email")
	}
	if month, _ := monthOf(context.Background(), q, settingRemindersMonth); month != "" {
		t.Errorf("the dry run recorded the month as %q", month)
	}
}

// With email off the run fails as a whole, so the daemon does not record a
// month in which nobody was reminded.
func TestMembershipRemindersWithEmailOff(t *testing.T) {
	q, database, cfg := reminderDB(t, map[string]int{"two": 2})
	cfg.EmailEnabled = false
	if _, err := queueMembershipReminders(context.Background(), cfg, q, time.Now(), false, io.Discard); err == nil {
		t.Fatal("no error with email off")
	}
	if month, _ := monthOf(context.Background(), q, settingRemindersMonth); month != "" || outboxCount(t, database) != 0 {
		t.Errorf("month %q recorded, %d queued", month, outboxCount(t, database))
	}
}

// The daemon charges a missed month within the first week, once.
func TestMonthlyTickChargesOnce(t *testing.T) {
	q, database, cfg := reminderDB(t, map[string]int{"member": 0})
	ctx := context.Background()
	now := time.Now()
	first := time.Date(now.Year(), now.Month(), 3, 9, 0, 0, 0, time.Local)
	previous := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local).AddDate(0, -1, 0)
	if err := recordMonth(ctx, q, settingFeesMonth, previous); err != nil {
		t.Fatal(err)
	}
	if err := recordMonth(ctx, q, settingRemindersMonth, previous); err != nil {
		t.Fatal(err)
	}

	var state daemonState
	monthlyTick(ctx, cfg, q, &state, first)
	monthlyTick(ctx, cfg, q, &state, first.Add(2*time.Minute))

	var fees int
	if err := database.QueryRow(`SELECT COUNT(*) FROM fees`).Scan(&fees); err != nil {
		t.Fatal(err)
	}
	if fees != 1 {
		t.Errorf("%d fees after two ticks, want 1", fees)
	}
	if month, _ := monthOf(ctx, q, settingFeesMonth); month != first.Format("2006-01") {
		t.Errorf("fees month %q", month)
	}
	if month, _ := monthOf(ctx, q, settingRemindersMonth); month != first.Format("2006-01") {
		t.Errorf("reminders month %q", month)
	}
	if outboxCount(t, database) != 1 {
		t.Errorf("%d reminders, want the one for the month just charged", outboxCount(t, database))
	}
}
