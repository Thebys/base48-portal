package email

import (
	"context"

	"github.com/base48/member-portal/internal/db"
)

// goldenSends queues each email the way the portal does, for the golden test.
var goldenSends = map[string]func(*Client, *db.User) error{
	"registration": func(c *Client, u *db.User) error { return c.SendRegistration(context.Background(), u) },
	"welcome":      func(c *Client, u *db.User) error { return c.SendWelcome(context.Background(), u) },
	"membership_suspended": func(c *Client, u *db.User) error {
		return c.SendMembershipSuspended(context.Background(), u, "")
	},
	"negative_balance": func(c *Client, u *db.User) error {
		return c.SendDebtReminder(context.Background(), u, TierNegativeBalance, -1000, 1000)
	},
	"debt_warning": func(c *Client, u *db.User) error {
		return c.SendDebtReminder(context.Background(), u, TierDebtWarning, -2400, 1000)
	},
	"bar_debt": func(c *Client, u *db.User) error { return c.SendBarDebt(context.Background(), u, -35050, 200) },
}
