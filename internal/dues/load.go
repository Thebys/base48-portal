package dues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/base48/member-portal/internal/db"
)

// The month the monthly run last charged fees and queued reminders, kept in
// settings as "2006-01" by portal-cron.
const (
	SettingFeesMonth      = "membership_fees_month"
	SettingRemindersMonth = "membership_reminders_month"
)

// RunMonth is the last month the monthly run charged, as of now. Until the
// run has recorded one, it is the latest month anybody was charged for, not
// counting fees an admin entered ahead.
func RunMonth(ctx context.Context, q *db.Queries, now time.Time) (Month, error) {
	setting, err := q.GetSetting(ctx, SettingFeesMonth)
	switch {
	case err == nil:
		return ParseMonth(setting.Value)
	case !errors.Is(err, sql.ErrNoRows):
		return Month{}, fmt.Errorf("read %s: %w", SettingFeesMonth, err)
	}
	latest, err := q.GetLatestChargedMonth(ctx, MonthOf(now).String())
	if err != nil {
		return Month{}, fmt.Errorf("latest charged month: %w", err)
	}
	if latest == "" {
		return Month{}, nil
	}
	return ParseMonth(latest)
}

// Anchor is the later of a member's last fee month ("" for none) and the
// run month.
func Anchor(lastFee string, run Month) Month {
	m, err := ParseMonth(lastFee)
	if err != nil || m.Before(run) {
		return run
	}
	return m
}

// Load is one member's standing.
func Load(ctx context.Context, q *db.Queries, user *db.User, levelAmount string, now time.Time) (Standing, error) {
	balance, err := q.GetUserBalance(ctx, db.GetUserBalanceParams{
		UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
		UserID_2: user.ID,
	})
	if err != nil {
		return Standing{}, fmt.Errorf("balance: %w", err)
	}
	lastFee, err := q.GetUserLastFeeMonth(ctx, user.ID)
	if err != nil {
		return Standing{}, fmt.Errorf("last fee: %w", err)
	}
	run, err := RunMonth(ctx, q, now)
	if err != nil {
		return Standing{}, err
	}
	return Standing{
		Fee:     MonthlyFee(user.LevelActualAmount, levelAmount),
		Balance: float64(balance),
		Anchor:  Anchor(lastFee, run),
	}, nil
}

// LoadAll is the standing of every member in users, by user id, in three
// queries however many members there are.
func LoadAll(ctx context.Context, q *db.Queries, users []db.ListAcceptedUsersForFeesRow, now time.Time) (map[int64]Standing, error) {
	balances, err := q.ListUserBalances(ctx)
	if err != nil {
		return nil, fmt.Errorf("balances: %w", err)
	}
	byUser := make(map[int64]float64, len(balances))
	for _, b := range balances {
		byUser[b.UserID] = float64(b.Balance)
	}
	lastFees, err := q.ListLastFeeMonths(ctx)
	if err != nil {
		return nil, fmt.Errorf("last fees: %w", err)
	}
	lastByUser := make(map[int64]string, len(lastFees))
	for _, f := range lastFees {
		lastByUser[f.UserID] = f.Month
	}
	run, err := RunMonth(ctx, q, now)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]Standing, len(users))
	for _, u := range users {
		out[u.ID] = Standing{
			Fee:     MonthlyFee(u.LevelActualAmount, u.LevelAmount),
			Balance: byUser[u.ID],
			Anchor:  Anchor(lastByUser[u.ID], run),
		}
	}
	return out, nil
}
