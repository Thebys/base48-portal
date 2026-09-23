package email

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/base48/member-portal/internal/db"
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

// queueDebt queues the reminder for the given tier and returns the outbox
// entry as the worker would see it.
func queueDebt(t *testing.T, c *Client, database *sql.DB, userID int64, tier string, balance float64) db.EmailOutbox {
	t.Helper()
	ctx := context.Background()
	user, err := c.queries.GetUserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Scheduled(0).SendDebtReminder(ctx, &user, tier, balance, 1000); err != nil {
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
	entry := queueDebt(t, c, database, user, TierNegativeBalance, -2000)

	pay(t, database, user, "payer", 2000) // paid during the 72h review window

	if c.refreshBeforeSend(context.Background(), &entry) {
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
	entry := queueDebt(t, c, database, user, TierDebtWarning, -3000)

	pay(t, database, user, "partial", 1500) // now -1500: one fee, not two

	if !c.refreshBeforeSend(context.Background(), &entry) {
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
	if !strings.Contains(html, "-1500&nbsp;Kč") || strings.Contains(html, "-3000") {
		t.Error("stored html should quote the current -1500, not the queued -3000")
	}
}

func TestDebtRefreshCancelsForSuspendedMember(t *testing.T) {
	c, database := barTestClient(t)
	user := seedDebtor(t, database, "leaver", 2)
	entry := queueDebt(t, c, database, user, TierNegativeBalance, -2000)

	if _, err := database.Exec(`UPDATE users SET state = 'suspended' WHERE id = ?`, user); err != nil {
		t.Fatal(err)
	}
	if c.refreshBeforeSend(context.Background(), &entry) {
		t.Fatal("a suspended member must not get a fee reminder")
	}
}
