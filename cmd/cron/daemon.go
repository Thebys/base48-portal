package main

import (
	"context"
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
//   - Monthly fees: once on the 1st of each month (checked every tick, idempotent)
//   - Bar debt emails: once a day from barDebtHour (no-op unless enabled in admin)
//
// Usage:
//
//	portal-cron daemon
//
// Systemd service (replaces two crontab entries):
//
//	ExecStart=/path/to/portal-cron daemon
func runDaemon(ctx context.Context, cfg *config.Config, queries *db.Queries) int {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Println("Starting portal-cron daemon")
	log.Printf("  Sync: every %v", syncInterval)
	log.Println("  Fees: 1st of each month")
	log.Printf("  Bar debt emails: daily from %d:00", barDebtHour)

	var lastFeesMonth time.Month
	var lastBarDebtDay string

	// Run immediately on startup
	daemonTick(ctx, cfg, queries, &lastFeesMonth, &lastBarDebtDay)

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			daemonTick(ctx, cfg, queries, &lastFeesMonth, &lastBarDebtDay)
		case <-ctx.Done():
			log.Println("Daemon shutting down...")
			return 0
		}
	}
}

func daemonTick(ctx context.Context, cfg *config.Config, queries *db.Queries, lastFeesMonth *time.Month, lastBarDebtDay *string) {
	now := time.Now()

	// On the 1st of each month, run fees then queue debt emails
	if now.Day() == 1 && *lastFeesMonth != now.Month() {
		log.Println("\n" + repeat("=", 80))
		log.Println("MONTHLY FEES (1st of month, auto)")
		log.Println(repeat("=", 80))
		runFees(ctx, cfg, queries)

		log.Println("\n" + repeat("-", 80))
		log.Println("DEBT EMAILS (after fees)")
		log.Println(repeat("-", 80))
		runDebtEmails(ctx, cfg, queries)

		*lastFeesMonth = now.Month()
	}

	// Once a day, queue bar debt reminders. Before the sync so that a
	// reminder queued with a zero delay goes out in this same tick.
	if today := now.Format("2006-01-02"); now.Hour() >= barDebtHour && *lastBarDebtDay != today {
		log.Println("\n" + repeat("-", 80))
		log.Println("BAR DEBT EMAILS (daily)")
		log.Println(repeat("-", 80))
		runBarDebtEmails(ctx, cfg, queries)
		*lastBarDebtDay = today
	}

	// Sync runs every tick
	runSync(ctx, cfg, queries)
}
