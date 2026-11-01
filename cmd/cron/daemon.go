package main

import (
	"context"
	"io"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
)

const syncInterval = 2 * time.Minute

// barDebtHour is the local hour from which the daily bar debt check runs. With
// the default outbox delay of 24h, reminders then leave around the same
// daytime hour rather than whenever the last purchase happened to sync.
const barDebtHour = 10

// runDaemon runs all cron jobs on schedule in a single long-running process.
//
// Schedule:
//   - FIO sync + debt/role update + emails: every 2 minutes
//   - Monthly fees, then membership reminders: on the 1st, made up within the
//     first week if the daemon was down (see monthlyDue)
//   - Bar debt emails: once a day from barDebtHour (no-op unless enabled in admin)
//
// Usage:
//
//	portal-cron daemon
func runDaemon(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Println("Starting portal-cron daemon")
	log.Printf("  Sync: every %v", syncInterval)
	log.Printf("  Fees and membership reminders: 1st of each month, made up until day %d", catchUpDays)
	log.Printf("  Bar debt emails: daily from %d:00", barDebtHour)

	var state daemonState

	// Run immediately on startup
	daemonTick(ctx, cfg, queries, &state)

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			daemonTick(ctx, cfg, queries, &state)
		case <-ctx.Done():
			log.Println("Daemon shutting down...")
			return 0
		}
	}
}

// daemonState is what the daemon remembers between ticks. The monthly run
// itself is recorded in the database, so a restart does not repeat it.
type daemonState struct {
	barDebtDay     string    // day the bar reminders last ran
	remindersAfter time.Time // reminders failed; not before this
	missedWarned   string    // month a missed fee run was last logged for
}

// reminderRetry is how long the daemon waits after membership reminders could
// not be queued (no payment account known yet, say) before trying again.
const reminderRetry = time.Hour

func daemonTick(ctx context.Context, cfg *config.Config, queries *db.Queries, state *daemonState) {
	now := time.Now()

	monthlyTick(ctx, cfg, queries, state, now)

	// Once a day, queue bar debt reminders. Before the sync so that a
	// reminder queued with a zero delay goes out in this same tick.
	if today := now.Format("2006-01-02"); now.Hour() >= barDebtHour && state.barDebtDay != today {
		log.Println("\n" + repeat("-", 80))
		log.Println("BAR DEBT EMAILS (daily)")
		log.Println(repeat("-", 80))
		runBarDebtEmails(ctx, cfg, queries)
		state.barDebtDay = today
	}

	// Sync runs every tick
	runSync(ctx, cfg, queries)
}

// monthlyTick charges the month's fees and queues the membership reminders
// when monthlyDue says so.
func monthlyTick(ctx context.Context, cfg *config.Config, queries *db.Queries, state *daemonState, now time.Time) {
	feesMonth, err := monthOf(ctx, queries, settingFeesMonth)
	if err != nil {
		log.Printf("✗ Monthly run: %v", err)
		return
	}
	remindersMonth, err := monthOf(ctx, queries, settingRemindersMonth)
	if err != nil {
		log.Printf("✗ Monthly run: %v", err)
		return
	}

	plan := monthlyDue(now, feesMonth, remindersMonth)
	if plan.missed != "" && state.missedWarned != now.Format("2006-01") {
		log.Printf("⚠ Monthly fees: %s; nothing is made up automatically, an admin has to look", plan.missed)
		queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "cron",
			Level:     "error",
			Message:   "Měsíční předpis: " + plan.missed + ". Doplnit ho musí admin.",
		})
		state.missedWarned = now.Format("2006-01")
	}

	if plan.fees {
		log.Println("\n" + repeat("=", 80))
		log.Println("MONTHLY FEES")
		log.Println(repeat("=", 80))
		if _, err := chargeMonthlyFees(ctx, queries, now); err != nil {
			log.Printf("✗ Fees not created: %v", err)
			return
		}
	}

	if plan.reminders && !now.Before(state.remindersAfter) {
		log.Println("\n" + repeat("-", 80))
		log.Println("MEMBERSHIP REMINDERS (after fees)")
		log.Println(repeat("-", 80))
		if _, err := queueMembershipReminders(ctx, cfg, queries, now, false, io.Discard); err != nil {
			log.Printf("✗ Membership reminders not queued, trying again in %v: %v", reminderRetry, err)
			state.remindersAfter = now.Add(reminderRetry)
		}
	}
}
