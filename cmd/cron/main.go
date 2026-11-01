package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: portal-cron <command>\n\nCommands:\n  daemon       Run all jobs on schedule (sync every 2min, fees on 1st of month)\n  sync         Sync FIO payments, update debt/roles, send pending emails\n  fees         Create monthly membership fees\n  debt-emails  Queue debt warning emails for members with negative balance\n  bar-debt-emails  Queue bar debt reminders (settings in admin)\n  report       Report unmatched payments\n  fio-backfill Record older outgoing FIO payments (fio-backfill YYYY-MM-DD, or - < statement.json)\n  fio-balances Load account balances from a saved FIO statement (fio-balances - < statement.json)\n")
		os.Exit(1)
	}

	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg.KeycloakReadOnly {
		log.Printf("ℹ KEYCLOAK_READ_ONLY: Keycloak is read-only, role changes are not written")
	}
	if len(cfg.RetiredEnv) > 0 {
		log.Printf("ℹ %s set but no longer used: the payment account comes from the FIO sync", strings.Join(cfg.RetiredEnv, ", "))
	}

	database, err := sql.Open("sqlite", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	queries := db.New(database)
	ctx := context.Background()

	switch os.Args[1] {
	case "sync":
		os.Exit(runSync(ctx, cfg, queries))
	case "fees":
		os.Exit(runFees(ctx, cfg, queries))
	case "debt-emails":
		os.Exit(runDebtEmails(ctx, cfg, queries))
	case "bar-debt-emails":
		os.Exit(runBarDebtEmails(ctx, cfg, queries))
	case "report":
		os.Exit(runReport(ctx, cfg, queries))
	case "fio-backfill":
		os.Exit(runFIOBackfill(ctx, cfg, queries, os.Args[2:], os.Stdin))
	case "fio-balances":
		os.Exit(runFIOBalances(ctx, queries, os.Args[2:], os.Stdin))
	case "daemon":
		os.Exit(runDaemon(ctx, cfg, queries))
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func repeat(s string, count int) string {
	return strings.Repeat(s, count)
}
