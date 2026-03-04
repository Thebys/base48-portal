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

// runDaemon runs all cron jobs on schedule in a single long-running process.
//
// Schedule:
//   - FIO sync + debt/role update + emails: every 2 minutes
//   - Monthly fees: once on the 1st of each month (checked every tick, idempotent)
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

	var lastFeesMonth time.Month

	// Run immediately on startup
	daemonTick(ctx, cfg, queries, &lastFeesMonth)

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			daemonTick(ctx, cfg, queries, &lastFeesMonth)
		case <-ctx.Done():
			log.Println("Daemon shutting down...")
			return 0
		}
	}
}

func daemonTick(ctx context.Context, cfg *config.Config, queries *db.Queries, lastFeesMonth *time.Month) {
	now := time.Now()

	// On the 1st of each month, run fees then queue debt emails
	if now.Day() == 1 && *lastFeesMonth != now.Month() {
		log.Printf("\n" + repeat("=", 80))
		log.Println("MONTHLY FEES (1st of month, auto)")
		log.Println(repeat("=", 80))
		runFees(ctx, cfg, queries)

		log.Printf("\n" + repeat("-", 80))
		log.Println("DEBT EMAILS (after fees)")
		log.Println(repeat("-", 80))
		runDebtEmails(ctx, cfg, queries)

		*lastFeesMonth = now.Month()
	}

	// Sync runs every tick
	runSync(ctx, cfg, queries)
}
