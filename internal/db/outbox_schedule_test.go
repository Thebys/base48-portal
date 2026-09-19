package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newOutboxDB creates just enough schema to exercise the outbox queries.
func newOutboxDB(t *testing.T) *Queries {
	t.Helper()
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	_, err = d.Exec(`CREATE TABLE email_outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		recipient TEXT NOT NULL,
		subject TEXT NOT NULL,
		template_name TEXT NOT NULL,
		template_data TEXT,
		rendered_html TEXT,
		status TEXT NOT NULL DEFAULT 'pending'
			CHECK (status IN ('pending', 'sent', 'failed', 'cancelled')),
		attempts INTEGER NOT NULL DEFAULT 0,
		max_attempts INTEGER NOT NULL DEFAULT 3,
		last_error TEXT,
		next_retry_at TIMESTAMP,
		sent_at TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return New(d)
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

// A worker that dies after claiming must not strand the email forever.
func TestClaimLeaseExpires(t *testing.T) {
	q := newOutboxDB(t)
	entry := queue(t, q, "retry@base48.cz", "debt_warning.html", 0)

	if _, err := q.ClaimScheduledEmails(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Simulate the lease ageing out instead of waiting five real minutes.
	reclaimed, err := q.UpdateEmailOutboxStatus(context.Background(), UpdateEmailOutboxStatusParams{
		Status:      "pending",
		Attempts:    0,
		NextRetryAt: sql.NullTime{Time: time.Now().UTC().Add(-time.Second), Valid: true},
		ID:          entry.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Status != "pending" {
		t.Fatalf("status = %q, want pending", reclaimed.Status)
	}

	again, err := q.ClaimScheduledEmails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Errorf("claimed %d after the lease expired, want 1", len(again))
	}
}

func TestListRecentDebtEmailsOnlyReturnsReminders(t *testing.T) {
	q := newOutboxDB(t)
	queue(t, q, "a@base48.cz", "debt_warning.html", 0)
	queue(t, q, "b@base48.cz", "negative_balance.html", 0)
	queue(t, q, "c@base48.cz", "welcome.html", 0)

	ctx := context.Background()
	// user_id is what the picker folds on; the helper leaves it NULL, so set it.
	for i, id := range []int64{1, 2, 3} {
		if _, err := q.db.ExecContext(ctx, `UPDATE email_outbox SET user_id = ? WHERE id = ?`, id, i+1); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := q.ListRecentDebtEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 — welcome.html is not a debt reminder", len(rows))
	}
	for _, row := range rows {
		if row.TemplateName == "welcome.html" {
			t.Errorf("welcome.html leaked into the debt reminder history")
		}
	}
}
