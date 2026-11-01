package email

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
)

// seedDebtor creates an accepted member on a 1000 Kč level who owes `months`
// monthly fees, and returns their id.
func seedDebtor(t *testing.T, database *sql.DB, name string, months int) int64 {
	t.Helper()
	var levelID, userID int64
	if err := database.QueryRow(
		`INSERT INTO levels (name, amount, active) VALUES (?, 1000, 1) RETURNING id`, "lvl-"+name,
	).Scan(&levelID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(
		`INSERT INTO users (email, username, level_id, payments_id, state)
		 VALUES (?, ?, ?, ?, 'accepted') RETURNING id`,
		name+"@base48.cz", name, levelID, "VS"+name,
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for m := 1; m <= months; m++ {
		if _, err := database.Exec(
			`INSERT INTO fees (user_id, level_id, amount, period_start) VALUES (?, ?, 1000, ?)`,
			userID, levelID, fmt.Sprintf("2026-%02d-01", m),
		); err != nil {
			t.Fatal(err)
		}
	}
	return userID
}

func pay(t *testing.T, database *sql.DB, userID int64, name string, amount int) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO payments (user_id, amount, identification, date, kind, kind_id, local_account, remote_account)
		 VALUES (?, ?, ?, '2026-09-05', 'manual', ?, '', '')`,
		userID, amount, "VS"+name, fmt.Sprintf("pay-%s-%d", name, amount),
	); err != nil {
		t.Fatal(err)
	}
}

// standingOf is the member's standing as the reminders see it.
func standingOf(t *testing.T, c *Client, user *db.User) dues.Standing {
	t.Helper()
	st, err := dues.Load(context.Background(), c.queries, user, "1000", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// queueDebt queues the reminder for the given tier and returns the outbox
// entry as the worker would see it.
func queueDebt(t *testing.T, c *Client, database *sql.DB, userID int64, tier string) db.EmailOutbox {
	t.Helper()
	ctx := context.Background()
	user, err := c.queries.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Scheduled(0).SendMembershipReminder(ctx, &user, tier, standingOf(t, c, &user)); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := database.QueryRow(`SELECT MAX(id) FROM email_outbox WHERE user_id = ?`, userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	entry, err := c.queries.GetEmailOutbox(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestDebtRefreshCancelsWhenPaid(t *testing.T) {
	c, database := barTestClient(t)
	user := seedDebtor(t, database, "payer", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance)

	pay(t, database, user, "payer", 2000) // paid during the 72h review window

	if _, ok := c.refreshBeforeSend(context.Background(), &entry); ok {
		t.Fatal("a member who paid up must not get the reminder")
	}
	status, lastErr, _ := outboxState(t, database, entry.ID)
	if status != "cancelled" || !strings.Contains(lastErr, "uhrazen") {
		t.Errorf("status=%q last_error=%q, want cancelled with the reason", status, lastErr)
	}
}

// Paying part of a debt warning drops the member to the milder tier; they
// should get that email, not the warning they no longer deserve.
func TestDebtRefreshDowngradesTier(t *testing.T) {
	c, database := barTestClient(t)
	user := seedDebtor(t, database, "partial", 3)
	entry := queueDebt(t, c, database, user, TierDebtWarning)

	pay(t, database, user, "partial", 1500) // now -1500: one fee, not two

	if _, ok := c.refreshBeforeSend(context.Background(), &entry); !ok {
		t.Fatal("still in debt, should be sent")
	}
	if entry.TemplateName != "negative_balance.html" {
		t.Errorf("template = %q, want negative_balance.html", entry.TemplateName)
	}
	if entry.Subject != defaultContentBlocks[TierNegativeBalance]["subject"] {
		t.Errorf("subject = %q, want the negative-balance one", entry.Subject)
	}

	var tmpl, subject, html string
	if err := database.QueryRow(`SELECT template_name, subject, rendered_html FROM email_outbox WHERE id = ?`, entry.ID).
		Scan(&tmpl, &subject, &html); err != nil {
		t.Fatal(err)
	}
	if tmpl != "negative_balance.html" || subject != entry.Subject {
		t.Errorf("stored template=%q subject=%q, not updated", tmpl, subject)
	}
	if !strings.Contains(html, "−1&nbsp;500&nbsp;Kč") || strings.Contains(html, "−3&nbsp;000") {
		t.Error("stored html should quote the current −1 500, not the queued −3 000")
	}
}

func TestDebtRefreshCancelsForSuspendedMember(t *testing.T) {
	c, database := barTestClient(t)
	user := seedDebtor(t, database, "leaver", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance)

	if _, err := database.Exec(`UPDATE users SET state = 'suspended' WHERE id = ?`, user); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.refreshBeforeSend(context.Background(), &entry); ok {
		t.Fatal("a suspended member must not get a fee reminder")
	}
}

// The cron worker re-renders a reminder just before it leaves. It used to be
// built without the QR service, so every reminder it sent lost its QR code.
// Payment details now come from the recorded account in every process.
func TestDebtRefreshKeepsPaymentDetails(t *testing.T) {
	c, database := barTestClient(t)
	user := seedDebtor(t, database, "qr", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance)

	worker := New(c.config, c.queries) // what cmd/cron builds for the outbox
	if _, ok := worker.refreshBeforeSend(context.Background(), &entry); !ok {
		t.Fatal("still in debt, should be sent")
	}
	_, _, html := outboxState(t, database, entry.ID)
	for _, want := range []string{"/api/qr?vs=VSqr", "2003436319/2010", "CZ66 2010 0000 0020 0343 6319", "FIOBCZPPXXX"} {
		if !strings.Contains(html, want) {
			t.Errorf("re-rendered reminder lacks %q", want)
		}
	}
	if strings.Contains(html, "2800691518") {
		t.Error("re-rendered reminder quotes the account that never existed")
	}
}

// Without a recorded account a reminder can be neither queued nor sent: it
// would tell the member to pay without saying where.
func TestDebtReminderWaitsForPaymentAccount(t *testing.T) {
	c, database := barTestClient(t)
	user := seedDebtor(t, database, "early", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance)

	if _, err := database.Exec(`DELETE FROM settings WHERE key = 'bank_account'`); err != nil {
		t.Fatal(err)
	}

	u, err := c.queries.GetUserByID(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Scheduled(0).SendMembershipReminder(context.Background(), &u, TierNegativeBalance, standingOf(t, c, &u)); !errors.Is(err, bank.ErrUnknown) {
		t.Errorf("queueing without an account: err = %v, want bank.ErrUnknown", err)
	}

	if _, ok := c.refreshBeforeSend(context.Background(), &entry); ok {
		t.Fatal("a reminder without payment details must not be sent")
	}
	if status, _, _ := outboxState(t, database, entry.ID); status != "pending" {
		t.Errorf("status = %q, want it left pending for a later pass", status)
	}
}

// The outbox shows a pending reminder as the last look would send it now.
// It used to show the queue-time version, which the last look then replaced.
// Showing must not store anything: the entry is still the worker's to send.
func TestAsItWouldLeaveShowsTheReminderAsItWouldGoOut(t *testing.T) {
	c, database := barTestClient(t)
	ctx := context.Background()
	partial := queueDebt(t, c, database, seedDebtor(t, database, "partial", 3), TierDebtWarning)
	payer := seedDebtor(t, database, "payer", 2)
	paid := queueDebt(t, c, database, payer, TierNegativeBalance)

	pay(t, database, partial.UserID.Int64, "partial", 1500) // now -1500: one fee, not two
	pay(t, database, payer, "payer", 2000)

	shown := c.AsItWouldLeave(ctx, partial)
	if shown.TemplateName != "negative_balance.html" || !strings.Contains(shown.RenderedHtml.String, "−1&nbsp;500&nbsp;Kč") {
		t.Errorf("shown as %s, want the negative-balance reminder quoting −1 500", shown.TemplateName)
	}
	if shown = c.AsItWouldLeave(ctx, paid); !strings.Contains(shown.LastError.String, "zrušil by se: bilance je teď") {
		t.Errorf("paid reminder shown with last_error %q, want why it would be withdrawn", shown.LastError.String)
	}

	for _, queued := range []db.EmailOutbox{partial, paid} {
		status, lastErr, html := outboxState(t, database, queued.ID)
		if status != "pending" || lastErr != "" || html != queued.RenderedHtml.String {
			t.Errorf("outbox #%d changed by showing it: status=%q last_error=%q", queued.ID, status, lastErr)
		}
	}
}
