package email

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
)

// enablePrepaid switches the reminder before any debt on or off, and records
// this month as charged, as the monthly run does: the seeded fees are in the
// spring, and "paid up to the end of this month" counts from now.
func enablePrepaid(t *testing.T, c *Client, on bool) {
	t.Helper()
	if _, err := c.queries.UpsertSetting(context.Background(), db.UpsertSettingParams{
		Key: dues.SettingFeesMonth, Value: dues.MonthOf(time.Now()).String(),
	}); err != nil {
		t.Fatal(err)
	}
	s := DefaultMembershipSettings()
	s.PrepaidEnabled = on
	if err := SaveMembershipSettings(context.Background(), c.queries, s); err != nil {
		t.Fatal(err)
	}
}

// A member who pays after the reminder before any debt was queued does not
// get it.
func TestPrepaidRefreshCancelsWhenPaid(t *testing.T) {
	c, database := barTestClient(t)
	enablePrepaid(t, c, true)
	user := seedDebtor(t, database, "monthly", 1)
	pay(t, database, user, "monthly", 1000) // paid for the one month charged
	entry := queueDebt(t, c, database, user, TierPrepaidEnding)

	if _, ok := c.refreshBeforeSend(context.Background(), &entry); !ok {
		t.Fatal("still paid only to the end of the month, should be sent")
	}
	pay(t, database, user, "monthly", 3000)
	if _, ok := c.refreshBeforeSend(context.Background(), &entry); ok {
		t.Fatal("a member who paid ahead got the reminder")
	}
	if status, lastErr, _ := outboxState(t, database, entry.ID); status != "cancelled" || !strings.Contains(lastErr, "už má zaplaceno do konce") {
		t.Errorf("status %q, reason %q", status, lastErr)
	}
}

func TestPrepaidRefreshCancelsWhenSwitchedOff(t *testing.T) {
	c, database := barTestClient(t)
	enablePrepaid(t, c, true)
	user := seedDebtor(t, database, "monthly", 1)
	pay(t, database, user, "monthly", 1000)
	entry := queueDebt(t, c, database, user, TierPrepaidEnding)

	enablePrepaid(t, c, false)
	if _, ok := c.refreshBeforeSend(context.Background(), &entry); ok {
		t.Fatal("sent although switched off")
	}
	if _, lastErr, _ := outboxState(t, database, entry.ID); !strings.Contains(lastErr, "vypnuta") {
		t.Errorf("reason %q", lastErr)
	}
}

// A member who pays the debt the reminder was about is not then told to pay
// ahead: they have just acted.
func TestDebtReminderNeverTurnsIntoPrepaid(t *testing.T) {
	c, database := barTestClient(t)
	enablePrepaid(t, c, true)
	user := seedDebtor(t, database, "late", 1)
	entry := queueDebt(t, c, database, user, TierNegativeBalance)

	pay(t, database, user, "late", 1000) // paid exactly the debt: still not paid ahead
	if _, ok := c.refreshBeforeSend(context.Background(), &entry); ok {
		t.Fatal("the debt reminder went out as another email")
	}
	if status, lastErr, _ := outboxState(t, database, entry.ID); status != "cancelled" || !strings.Contains(lastErr, "uhrazen") {
		t.Errorf("status %q, reason %q", status, lastErr)
	}
}

// The short variant reads its own subject and heading, and the HTML quotes
// what is missing.
func TestPrepaidShortVariant(t *testing.T) {
	c, database := barTestClient(t)
	enablePrepaid(t, c, true)
	user := seedDebtor(t, database, "short", 1)
	pay(t, database, user, "short", 700)
	entry := queueDebt(t, c, database, user, TierPrepaidEnding)

	if !strings.Contains(entry.Subject, "chybí 300") {
		t.Errorf("subject %q", entry.Subject)
	}
	if !strings.Contains(entry.RenderedHtml.String, "300&nbsp;Kč") || strings.Contains(entry.RenderedHtml.String, "{") {
		t.Error("the short variant does not quote 300 Kč, or left a placeholder")
	}
}
