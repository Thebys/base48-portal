package email

import (
	"context"
	"time"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
)

// goldenSends queues each email the way the portal does, for the golden test.
// The membership reminders get a fixed standing in November 2026.
var goldenSends = map[string]func(*Client, *db.User) error{
	"registration": func(c *Client, u *db.User) error { return c.SendRegistration(context.Background(), u) },
	"welcome":      func(c *Client, u *db.User) error { return c.SendWelcome(context.Background(), u) },
	"membership_suspended": func(c *Client, u *db.User) error {
		return c.SendMembershipSuspended(context.Background(), u, "")
	},
	"prepaid_ending":       reminder(TierPrepaidEnding, 0),
	"prepaid_ending_short": reminder(TierPrepaidEnding, -300),
	"negative_balance":     reminder(TierNegativeBalance, -1000),
	"debt_warning":         reminder(TierDebtWarning, -2400),
	"bar_debt":             func(c *Client, u *db.User) error { return c.SendBarDebt(context.Background(), u, -35050, 200) },
}

func reminder(tier string, balance float64) func(*Client, *db.User) error {
	return func(c *Client, u *db.User) error {
		st := dues.Standing{Fee: 1000, Balance: balance, Anchor: dues.Month{Year: 2026, Month: time.November}}
		return c.SendMembershipReminder(context.Background(), u, tier, st)
	}
}
