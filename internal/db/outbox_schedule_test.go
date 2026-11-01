package db

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/migrate"
	"github.com/base48/member-portal/migrations"
)

// newOutboxDB creates a database with the real schema, so the outbox queries
// run against the columns the migrations actually create.
func newOutboxDB(t *testing.T) *Queries {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+t.TempDir()+"/outbox.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := migrate.Run(d, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(d)
}

// member inserts a user the outbox rows can point at.
func member(t *testing.T, q *Queries, id int64) {
	t.Helper()
	if _, err := q.db.ExecContext(context.Background(),
		`INSERT INTO users (id, email, level_id, state) VALUES (?, ?, 1, 'accepted')`,
		id, fmt.Sprintf("m%d@base48.cz", id)); err != nil {
		t.Fatal(err)
	}
}

func queue(t *testing.T, q *Queries, recipient, template string, dueIn time.Duration) EmailOutbox {
	t.Helper()
	entry, err := q.CreateEmailOutbox(context.Background(), CreateEmailOutboxParams{
		Recipient:    recipient,
		Subject:      "test",
		TemplateName: template,
		Status:       "pending",
		MaxAttempts:  3,
		// Mirrors what QueueEmail does. UTC is the whole point of this test.
		NextRetryAt: sql.NullTime{Time: time.Now().UTC().Add(dueIn), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// A zero delay must be due right away. Writing local wall-clock time made this
// fail by the TZ offset — two hours in CEST — so "send now" sat for two hours.
func TestZeroDelayIsDueImmediately(t *testing.T) {
	q := newOutboxDB(t)
	queue(t, q, "now@base48.cz", "debt_warning.html", 0)

	claimed, err := q.ClaimScheduledEmails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d emails, want 1 — a zero-delay email is not due yet", len(claimed))
	}
	if claimed[0].Recipient != "now@base48.cz" {
		t.Errorf("claimed %q, want now@base48.cz", claimed[0].Recipient)
	}
}

func TestFutureEmailsAreNotClaimed(t *testing.T) {
	q := newOutboxDB(t)
	queue(t, q, "later@base48.cz", "debt_warning.html", 24*time.Hour)
	queue(t, q, "due@base48.cz", "negative_balance.html", -time.Minute)

	claimed, err := q.ClaimScheduledEmails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d emails, want only the overdue one", len(claimed))
	}
	if claimed[0].Recipient != "due@base48.cz" {
		t.Errorf("claimed %q, want due@base48.cz", claimed[0].Recipient)
	}
}

// The server and the cron daemon poll the same database. Claiming must be
// exclusive or a member gets the same reminder twice.
func TestClaimIsExclusive(t *testing.T) {
	q := newOutboxDB(t)
	queue(t, q, "once@base48.cz", "debt_warning.html", 0)

	first, err := q.ClaimScheduledEmails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.ClaimScheduledEmails(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(first) != 1 {
		t.Fatalf("first claim got %d, want 1", len(first))
	}
	if len(second) != 0 {
		t.Errorf("second claim got %d, want 0 — the lease did not hold", len(second))
	}
}

// expireLease ages the row's lease instead of waiting five real minutes.
func expireLease(t *testing.T, q *Queries, id int64) {
	t.Helper()
	if _, err := q.db.ExecContext(context.Background(),
		`UPDATE email_outbox SET leased_until = datetime('now', '-1 second') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

func status(t *testing.T, q *Queries, id int64) string {
	t.Helper()
	entry, err := q.GetEmailOutbox(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return entry.Status
}

// A worker that dies after claiming must not strand the email forever.
func TestClaimLeaseExpires(t *testing.T) {
	q := newOutboxDB(t)
	entry := queue(t, q, "retry@base48.cz", "debt_warning.html", 0)

	if _, err := q.ClaimScheduledEmails(context.Background()); err != nil {
		t.Fatal(err)
	}
	expireLease(t, q, entry.ID)

	again, err := q.ClaimScheduledEmails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Errorf("claimed %d after the lease expired, want 1", len(again))
	}
}

// An admin cancelling an email the worker is sending got "cancelled" in the
// table while the mail went out. Now the cancel is refused until the lease
// ends, and succeeds after.
func TestCancelIsRefusedWhileLeased(t *testing.T) {
	q := newOutboxDB(t)
	ctx := context.Background()
	entry := queue(t, q, "busy@base48.cz", "debt_warning.html", 0)
	reason := sql.NullString{String: "Zrušil admin", Valid: true}

	if _, err := q.ClaimScheduledEmails(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CancelEmailOutbox(ctx, CancelEmailOutboxParams{Reason: reason, ID: entry.ID}); err != sql.ErrNoRows {
		t.Fatalf("cancel of a leased email: err = %v, want no rows", err)
	}

	expireLease(t, q, entry.ID)
	cancelled, err := q.CancelEmailOutbox(ctx, CancelEmailOutboxParams{Reason: reason, ID: entry.ID})
	if err != nil {
		t.Fatalf("cancel after the lease: %v", err)
	}
	if cancelled.Status != "cancelled" || cancelled.LastError.String != "Zrušil admin" {
		t.Errorf("status %q, last_error %q", cancelled.Status, cancelled.LastError.String)
	}
}

// "Send now" and the worker must never both have a row.
func TestSendNowAndWorkerExcludeEachOther(t *testing.T) {
	q := newOutboxDB(t)
	ctx := context.Background()

	byWorker := queue(t, q, "worker@base48.cz", "debt_warning.html", 0)
	if _, err := q.ClaimScheduledEmails(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ClaimEmailOutbox(ctx, byWorker.ID); err != sql.ErrNoRows {
		t.Errorf("send-now got a row the worker holds: err = %v", err)
	}

	byAdmin := queue(t, q, "admin@base48.cz", "debt_warning.html", 72*time.Hour)
	if _, err := q.ClaimEmailOutbox(ctx, byAdmin.ID); err != nil {
		t.Fatalf("send-now of a scheduled email: %v", err)
	}
	if claimed, err := q.ClaimScheduledEmails(ctx); err != nil || len(claimed) != 0 {
		t.Errorf("worker claimed %d rows (err %v) while send-now holds the only free one", len(claimed), err)
	}

	// The sender died: once the lease ends the worker delivers it, as
	// send-now made it due at once.
	expireLease(t, q, byAdmin.ID)
	claimed, err := q.ClaimScheduledEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != byAdmin.ID {
		t.Errorf("after a crashed send-now the worker claimed %v, want #%d", claimed, byAdmin.ID)
	}
}

func TestFailureNeverOverwritesACancel(t *testing.T) {
	q := newOutboxDB(t)
	ctx := context.Background()
	entry := queue(t, q, "gone@base48.cz", "welcome.html", 0)

	if err := q.CancelLeasedEmailOutbox(ctx, CancelLeasedEmailOutboxParams{
		Reason: sql.NullString{String: "už není přijatý člen", Valid: true}, ID: entry.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkEmailFailed(ctx, MarkEmailFailedParams{
		Attempts: 3, LastError: sql.NullString{String: "smtp down", Valid: true}, ID: entry.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.RescheduleEmail(ctx, RescheduleEmailParams{Attempts: 1, Delay: "+10 minutes", ID: entry.ID}); err != nil {
		t.Fatal(err)
	}
	if got := status(t, q, entry.ID); got != "cancelled" {
		t.Errorf("status = %q, want cancelled", got)
	}
}

// A failed try leaves the email pending, due later, with the error recorded;
// a failed email goes back to the queue only by hand.
func TestRescheduleAndRequeue(t *testing.T) {
	q := newOutboxDB(t)
	ctx := context.Background()
	entry := queue(t, q, "flaky@base48.cz", "welcome.html", 0)

	if _, err := q.ClaimScheduledEmails(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.RescheduleEmail(ctx, RescheduleEmailParams{
		Attempts: 1, LastError: sql.NullString{String: "timeout", Valid: true}, Delay: "+10 minutes", ID: entry.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := q.ClaimScheduledEmails(ctx); len(claimed) != 0 {
		t.Error("a rescheduled email was due again at once")
	}
	got, err := q.GetEmailOutbox(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.Attempts != 1 || got.LastError.String != "timeout" || got.LeasedUntil.Valid {
		t.Errorf("after a failed try: %+v", got)
	}

	if _, err := q.RequeueFailedEmail(ctx, entry.ID); err != sql.ErrNoRows {
		t.Errorf("a pending email was requeued as failed: err = %v", err)
	}
	if err := q.MarkEmailFailed(ctx, MarkEmailFailedParams{Attempts: 3, ID: entry.ID}); err != nil {
		t.Fatal(err)
	}
	requeued, err := q.RequeueFailedEmail(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != "pending" || requeued.Attempts != 0 {
		t.Errorf("requeued: status %q attempts %d", requeued.Status, requeued.Attempts)
	}
	if claimed, _ := q.ClaimScheduledEmails(ctx); len(claimed) != 1 {
		t.Error("a requeued email is not due")
	}
}

// The monthly run looks for any membership reminder since the start of the
// month. The old 30-day window let a February reminder block March's.
func TestMembershipEmailSince(t *testing.T) {
	q := newOutboxDB(t)
	ctx := context.Background()
	member(t, q, 7)
	add := func(template, createdAt, status string) {
		t.Helper()
		entry := queue(t, q, "m@base48.cz", template, 0)
		if _, err := q.db.ExecContext(ctx,
			`UPDATE email_outbox SET user_id = 7, created_at = ?, status = ? WHERE id = ?`, createdAt, status, entry.ID); err != nil {
			t.Fatal(err)
		}
	}
	since := func(start string) error {
		_, err := q.GetMembershipEmailSince(ctx, GetMembershipEmailSinceParams{
			UserID: sql.NullInt64{Int64: 7, Valid: true}, Since: start,
		})
		return err
	}

	add("negative_balance.html", "2026-01-31 23:00:52", "sent") // the run of 1 February, CET
	if err := since("2026-02-28 23:00:00"); err != sql.ErrNoRows {
		t.Errorf("February's reminder blocks March: err = %v", err)
	}
	if err := since("2026-01-31 23:00:00"); err != nil {
		t.Errorf("February's reminder does not block a second February run: %v", err)
	}

	add("welcome.html", "2026-03-02 10:00:00", "sent")
	add("debt_warning.html", "2026-03-02 10:00:00", "cancelled")
	if err := since("2026-02-28 23:00:00"); err != sql.ErrNoRows {
		t.Errorf("a welcome or a cancelled reminder counts as this month's: err = %v", err)
	}
	add("debt_warning.html", "2026-03-04 09:00:00", "pending")
	if err := since("2026-02-28 23:00:00"); err != nil {
		t.Errorf("a pending reminder does not count: %v", err)
	}
}

func TestListRecentMembershipEmailsOnlyReturnsReminders(t *testing.T) {
	q := newOutboxDB(t)
	queue(t, q, "a@base48.cz", "debt_warning.html", 0)
	queue(t, q, "b@base48.cz", "negative_balance.html", 0)
	queue(t, q, "c@base48.cz", "welcome.html", 0)

	ctx := context.Background()
	// user_id is what the picker folds on; the helper leaves it NULL, so set it.
	for i, id := range []int64{1, 2, 3} {
		member(t, q, id)
		if _, err := q.db.ExecContext(ctx, `UPDATE email_outbox SET user_id = ? WHERE id = ?`, id, i+1); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := q.ListRecentMembershipEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 — welcome.html is not a membership reminder", len(rows))
	}
	for _, row := range rows {
		if row.TemplateName == "welcome.html" {
			t.Errorf("welcome.html leaked into the membership reminder history")
		}
	}
}
