package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/email"
	"github.com/base48/member-portal/internal/qrpay"
)

// seedMember creates an accepted member on a fee level, optionally with a
// payment, and returns their id. Fees are what push a balance negative.
func seedMember(t *testing.T, database *sql.DB, email, vs string, fee int, paid int) int64 {
	t.Helper()

	var levelID int64
	err := database.QueryRow(
		`INSERT INTO levels (name, amount, active) VALUES (?, ?, 1) RETURNING id`,
		"lvl-"+email, fee,
	).Scan(&levelID)
	if err != nil {
		t.Fatalf("insert level: %v", err)
	}

	var userID int64
	err = database.QueryRow(
		`INSERT INTO users (email, username, level_id, payments_id, state)
		 VALUES (?, ?, ?, ?, 'accepted') RETURNING id`,
		email, email, levelID, vs,
	).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}

	if _, err := database.Exec(
		`INSERT INTO fees (user_id, level_id, amount, period_start) VALUES (?, ?, ?, '2026-09-01')`,
		userID, levelID, fee,
	); err != nil {
		t.Fatalf("insert fee: %v", err)
	}

	if paid > 0 {
		if _, err := database.Exec(
			`INSERT INTO payments (user_id, amount, identification, date,
			                       kind, kind_id, local_account, remote_account)
			 VALUES (?, ?, ?, '2026-09-05', 'manual', ?, '', '')`,
			userID, paid, vs, "pay-"+vs,
		); err != nil {
			t.Fatalf("insert payment: %v", err)
		}
	}

	return userID
}

func TestBuildDebtCandidates(t *testing.T) {
	queries, database := testQueries(t)
	h := &Handler{queries: queries}
	ctx := context.Background()

	// Owes exactly one fee → negative balance.
	oweOne := seedMember(t, database, "oweone@base48.cz", "1001", 1000, 0)
	// Paid up → not a candidate at all.
	paidUp := seedMember(t, database, "paid@base48.cz", "1002", 1000, 1000)
	// Free membership → never a candidate, however negative.
	free := seedMember(t, database, "free@base48.cz", "1003", 0, 0)

	// Owes two fees → debt warning. Two fee rows, nothing paid.
	deep := seedMember(t, database, "deep@base48.cz", "1004", 1000, 0)
	var deepLevel int64
	if err := database.QueryRow(`SELECT level_id FROM users WHERE id = ?`, deep).Scan(&deepLevel); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO fees (user_id, level_id, amount, period_start) VALUES (?, ?, 1000, '2026-08-01')`,
		deep, deepLevel,
	); err != nil {
		t.Fatal(err)
	}

	candidates, err := h.buildDebtCandidates(ctx)
	if err != nil {
		t.Fatalf("buildDebtCandidates: %v", err)
	}

	byID := map[int64]DebtCandidate{}
	for _, c := range candidates {
		byID[c.UserID] = c
	}

	if _, ok := byID[paidUp]; ok {
		t.Error("a member who is paid up must not be a reminder candidate")
	}
	if _, ok := byID[free]; ok {
		t.Error("a member on a zero fee must not be a reminder candidate")
	}

	if got := byID[oweOne].Tier; got != "negative_balance" {
		t.Errorf("owing one fee gives tier %q, want negative_balance", got)
	}
	if got := byID[deep].Tier; got != "debt_warning" {
		t.Errorf("owing two fees gives tier %q, want debt_warning", got)
	}
	if got := byID[deep].Balance; got != -2000 {
		t.Errorf("deep debtor balance = %d, want -2000", got)
	}
	if got := byID[deep].MonthlyFee; got != 1000 {
		t.Errorf("deep debtor monthly fee = %d, want 1000", got)
	}

	// Deepest debt sorts first, because that is the order an admin works through.
	if len(candidates) < 2 || candidates[0].UserID != deep {
		t.Errorf("candidates are not sorted by debt: got %+v", candidates)
	}

	// Nobody has been reminded yet, so nothing is blocked.
	for _, c := range candidates {
		if c.Blocked {
			t.Errorf("%s is blocked with no reminder history: %s", c.Email, c.BlockReason)
		}
	}
}

func TestBuildDebtCandidatesFlagsPriorReminders(t *testing.T) {
	queries, database := testQueries(t)
	h := &Handler{queries: queries}
	ctx := context.Background()

	recent := seedMember(t, database, "recent@base48.cz", "2001", 1000, 0)
	old := seedMember(t, database, "old@base48.cz", "2002", 1000, 0)
	waiting := seedMember(t, database, "waiting@base48.cz", "2003", 1000, 0)

	reminders := []struct {
		user   int64
		status string
		age    time.Duration
	}{
		{recent, "sent", 5 * 24 * time.Hour},
		{old, "sent", 60 * 24 * time.Hour},
		{waiting, "pending", 1 * time.Hour},
	}
	for _, r := range reminders {
		_, err := database.Exec(
			`INSERT INTO email_outbox (user_id, recipient, subject, template_name, status, created_at)
			 VALUES (?, 'x@base48.cz', 's', 'negative_balance.html', ?, ?)`,
			r.user, r.status,
			time.Now().UTC().Add(-r.age).Format("2006-01-02 15:04:05"),
		)
		if err != nil {
			t.Fatalf("insert outbox row: %v", err)
		}
	}

	candidates, err := h.buildDebtCandidates(ctx)
	if err != nil {
		t.Fatalf("buildDebtCandidates: %v", err)
	}
	byID := map[int64]DebtCandidate{}
	for _, c := range candidates {
		byID[c.UserID] = c
	}

	if c := byID[recent]; !c.Blocked || c.BlockReason == "" {
		t.Errorf("a reminder 5 days old should block: blocked=%v reason=%q", c.Blocked, c.BlockReason)
	}
	if c := byID[recent]; c.LastRemindedDays != 5 {
		t.Errorf("LastRemindedDays = %d, want 5", c.LastRemindedDays)
	}
	if c := byID[old]; c.Blocked {
		t.Errorf("a reminder 60 days old is outside the window and must not block: %q", c.BlockReason)
	}
	if c := byID[old]; c.LastRemindedAt.IsZero() {
		t.Error("an old reminder should still be reported in LastRemindedAt")
	}
	if c := byID[waiting]; !c.HasPending || !c.Blocked {
		t.Errorf("a pending reminder must block: pending=%v blocked=%v", c.HasPending, c.Blocked)
	}
	if c := byID[waiting]; c.BlockReason != "už čeká v outboxu" {
		t.Errorf("pending reason = %q, want the outbox wording", c.BlockReason)
	}
}

// The picker's history folds on the outbox template_name, so the two must agree.
func TestDebtReminderHistoryIgnoresOtherTemplates(t *testing.T) {
	queries, database := testQueries(t)
	h := &Handler{queries: queries}

	user := seedMember(t, database, "welcome@base48.cz", "3001", 1000, 0)
	if _, err := database.Exec(
		`INSERT INTO email_outbox (user_id, recipient, subject, template_name, status)
		 VALUES (?, 'x@base48.cz', 's', 'welcome.html', 'sent')`, user,
	); err != nil {
		t.Fatal(err)
	}

	history := h.loadDebtReminderHistory(context.Background())
	if entry, ok := history[user]; ok && !entry.lastAt.IsZero() {
		t.Error("a welcome email must not count as a debt reminder")
	}

	candidates, err := h.buildDebtCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.UserID == user && c.Blocked {
			t.Errorf("a welcome email blocked a debt reminder: %q", c.BlockReason)
		}
	}
}

// testHandlerWithEmail wires a handler against a throwaway database and a real
// email client pointed at the shipped templates. No SMTP host is configured, so
// nothing leaves the machine — the reminders land in the outbox, which is
// exactly what the manual sender is supposed to produce.
func testHandlerWithEmail(t *testing.T) (*Handler, *sql.DB) {
	t.Helper()

	queries, database := testQueries(t)
	cfg := &config.Config{
		EmailEnabled:  true,
		WebRoot:       "../../web",
		BaseURL:       "https://portal.base48.cz",
		BankAccountCZ: "2800691518/2010",
	}
	return &Handler{
		queries:     queries,
		config:      cfg,
		emailClient: email.New(cfg, queries, qrpay.NewService("", "")),
	}, database
}

func TestQueueDebtRemindersLandsInOutbox(t *testing.T) {
	h, database := testHandlerWithEmail(t)
	ctx := context.Background()

	owing := seedMember(t, database, "owing@base48.cz", "5001", 1000, 0)
	paidUp := seedMember(t, database, "ok@base48.cz", "5002", 1000, 1000)

	queued, skipped, err := h.queueDebtReminders(ctx, []int64{owing, paidUp, 9999}, 24*time.Hour, false)
	if err != nil {
		t.Fatalf("queueDebtReminders: %v", err)
	}

	if len(queued) != 1 || queued[0].UserID != owing {
		t.Fatalf("queued = %+v, want only the member in debt", queued)
	}
	if queued[0].Tier != "negative_balance" {
		t.Errorf("tier = %q, want negative_balance", queued[0].Tier)
	}
	// A member who is paid up and an id that does not exist are both reported
	// back rather than silently dropped.
	if len(skipped) != 2 {
		t.Fatalf("skipped = %+v, want the paid-up member and the unknown id", skipped)
	}

	var (
		recipient, template, status, html string
		nextRetry                         sql.NullString
	)
	err = database.QueryRow(
		`SELECT recipient, template_name, status, rendered_html, next_retry_at
		 FROM email_outbox WHERE user_id = ?`, owing,
	).Scan(&recipient, &template, &status, &html, &nextRetry)
	if err != nil {
		t.Fatalf("the reminder is not in the outbox: %v", err)
	}

	if recipient != "owing@base48.cz" {
		t.Errorf("recipient = %q", recipient)
	}
	if template != "negative_balance.html" {
		t.Errorf("template = %q, want negative_balance.html", template)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending — it must wait for its timer", status)
	}
	if !nextRetry.Valid || nextRetry.String == "" {
		t.Fatal("next_retry_at is unset, so the timer would never fire")
	}
	// The email has to quote the real debt, not a placeholder.
	if !strings.Contains(html, "-1000") {
		t.Errorf("rendered email does not mention the -1000 balance")
	}
	if !strings.Contains(html, "5001") {
		t.Errorf("rendered email does not carry the variable symbol")
	}

	// A 24h timer must land in the future by SQLite's own clock, or the email
	// would go out on the very next worker pass.
	var due bool
	if err := database.QueryRow(
		`SELECT datetime(next_retry_at) > datetime('now') FROM email_outbox WHERE user_id = ?`, owing,
	).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Errorf("a 24h timer is already due — next_retry_at %q vs SQLite now", nextRetry.String)
	}
}

func TestQueueDebtRemindersRespectsAndOverridesBlocks(t *testing.T) {
	h, database := testHandlerWithEmail(t)
	ctx := context.Background()

	member := seedMember(t, database, "again@base48.cz", "6001", 1000, 0)
	if _, err := database.Exec(
		`INSERT INTO email_outbox (user_id, recipient, subject, template_name, status, created_at)
		 VALUES (?, 'again@base48.cz', 's', 'negative_balance.html', 'sent', ?)`,
		member, time.Now().UTC().Add(-3*24*time.Hour).Format("2006-01-02 15:04:05"),
	); err != nil {
		t.Fatal(err)
	}

	queued, skipped, err := h.queueDebtReminders(ctx, []int64{member}, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 || len(skipped) != 1 {
		t.Fatalf("a member reminded 3 days ago should be skipped: queued=%+v skipped=%+v", queued, skipped)
	}

	queued, skipped, err = h.queueDebtReminders(ctx, []int64{member}, 24*time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || len(skipped) != 0 {
		t.Fatalf("force should override the block: queued=%+v skipped=%+v", queued, skipped)
	}
}

// The same id twice in one request must not produce two emails.
func TestQueueDebtRemindersDeduplicates(t *testing.T) {
	h, database := testHandlerWithEmail(t)
	ctx := context.Background()

	member := seedMember(t, database, "dupe@base48.cz", "7001", 1000, 0)

	queued, _, err := h.queueDebtReminders(ctx, []int64{member, member, member}, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Errorf("queued %d emails for one member repeated three times, want 1", len(queued))
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM email_outbox WHERE user_id = ?`, member).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("outbox holds %d rows for one member, want 1", count)
	}
}

// A zero timer still records the email; it is just due right away.
func TestQueueDebtRemindersZeroTimerIsDueNow(t *testing.T) {
	h, database := testHandlerWithEmail(t)
	ctx := context.Background()

	member := seedMember(t, database, "now@base48.cz", "8001", 1000, 0)

	queued, _, err := h.queueDebtReminders(ctx, []int64{member}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued = %+v, want 1", queued)
	}

	claimed, err := h.queries.ClaimScheduledEmails(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("a zero-timer reminder was not due for pickup (claimed %d)", len(claimed))
	}
	if claimed[0].Recipient != "now@base48.cz" {
		t.Errorf("claimed %q", claimed[0].Recipient)
	}
}

// With email switched off, QueueEmail swallows everything and returns success.
// Reporting that as a queued batch would send the admin looking for emails that
// were never written, so the endpoint refuses before it gets that far.
func TestQueueDebtRemindersEmailDisabledIsReportedNotSwallowed(t *testing.T) {
	h, database := testHandlerWithEmail(t)
	h.config.EmailEnabled = false

	member := seedMember(t, database, "off@base48.cz", "9101", 1000, 0)

	body, _ := json.Marshal(map[string]interface{}{
		"user_ids":    []int64{member},
		"delay_hours": 24,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/email/debt-notify", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AdminQueueDebtEmailsHandler(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "EMAIL_ENABLED") {
		t.Errorf("the response should name the switch that blocked it: %s", rec.Body.String())
	}

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM email_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("outbox holds %d rows, want 0", count)
	}
}
