package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
	"github.com/base48/member-portal/internal/email"
)

// reminderDelay is how long a membership reminder waits in the outbox for an
// admin to review it, and reminderHour the local hour it then leaves at.
const (
	reminderDelay = 72 * time.Hour
	reminderHour  = 10
)

// runDebtEmails queues this month's membership reminders and records the
// month as done (see monthlyDue). The daemon runs it right after the fees.
// Safe to run repeatedly: a member gets at most one membership reminder a
// calendar month. Each reminder waits about three days in the outbox, leaves
// at 10:00 and is re-checked against the live balance just before (see
// email.refreshBeforeSend).
//
// Usage:
//
//	portal-cron debt-emails [-dry-run]
func runDebtEmails(ctx context.Context, cfg *config.Config, queries *db.Queries, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("debt-emails", flag.ContinueOnError)
	flags.SetOutput(out)
	dryRun := flags.Bool("dry-run", false, "only list who would get which reminder")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	run, err := queueMembershipReminders(ctx, cfg, queries, time.Now(), *dryRun, out)
	if err != nil {
		log.Printf("✗ Membership reminders not queued: %v", err)
		return 1
	}
	if run.Errors > 0 {
		log.Println("Job completed with errors")
		return 1
	}
	return 0
}

// reminderRun counts one pass of queueMembershipReminders.
type reminderRun struct {
	Members int // accepted members on a paid level
	Queued  int
	Skipped int // already had a membership reminder this month
	NoDebt  int // no reminder warranted
	Errors  int
}

// queueMembershipReminders queues a reminder for every accepted member whose
// standing warrants one (dues.Classify, with the admin's settings) and who
// has not had one this calendar month. With
// dryRun it only writes to out who would get what. The error is set when the
// pass could not run at all: no payment account known, email switched off,
// members not listed. Then the month is not recorded and the daemon tries
// again later.
func queueMembershipReminders(ctx context.Context, cfg *config.Config, queries *db.Queries, now time.Time, dryRun bool, out io.Writer) (reminderRun, error) {
	var run reminderRun

	// Every reminder would fail the same way; say it once, where an admin looks.
	if _, ok := bank.Load(ctx, queries); !ok {
		queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "cron",
			Level:     "error",
			Message:   "Membership reminders not queued: " + bank.ErrUnknown.Error(),
		})
		return run, bank.ErrUnknown
	}
	if !cfg.EmailEnabled && !dryRun {
		return run, email.ErrEmailDisabled
	}

	users, err := queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		return run, fmt.Errorf("list members: %w", err)
	}
	standings, err := dues.LoadAll(ctx, queries, users, now)
	if err != nil {
		return run, fmt.Errorf("load standings: %w", err)
	}
	settings := email.LoadMembershipSettings(ctx, queries)
	month := dues.MonthOf(now)

	due := email.LocalHourAfter(now.Add(reminderDelay), reminderHour)
	client := email.New(cfg, queries).At(due)
	since := monthStartUTC(now)

	log.Printf("Membership reminders for %s, due %s (before any debt: %s)",
		month, due.Format("2006-01-02 15:04"), map[bool]string{true: "on", false: "off"}[settings.PrepaidEnabled])

	for _, user := range users {
		st := standings[user.ID]
		if !st.Known() {
			continue
		}
		run.Members++

		tier := dues.Classify(st, month, settings.Policy())
		if tier == dues.None {
			run.NoDebt++
			continue
		}

		// One membership reminder a calendar month, whichever kind. When the
		// history cannot be read, nothing is sent: a member mailed twice is
		// worse than one reminded on the next run.
		recent, err := queries.GetMembershipEmailSince(ctx, db.GetMembershipEmailSinceParams{
			UserID: sql.NullInt64{Int64: user.ID, Valid: true},
			Since:  since,
		})
		if err == nil {
			log.Printf("  ⊘ Skipping %s: %s already queued on %s", user.Email, recent.TemplateName, recent.CreatedAt.Format("2006-01-02"))
			run.Skipped++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("  ⚠ Cannot read reminder history of %s: %v", user.Email, err)
			run.Errors++
			continue
		}

		if dryRun {
			fmt.Fprintf(out, "%-18s %-40s %8.0f Kč  zaplaceno do %s  (příspěvek %.0f Kč)\n",
				tier, user.Email, st.Balance, st.PaidThrough(), st.Fee)
			run.Queued++
			continue
		}

		fullUser, err := queries.GetUserByID(ctx, user.ID)
		if err != nil {
			log.Printf("  ⚠ Failed to get user record for %s: %v", user.Email, err)
			run.Errors++
			continue
		}
		if err := client.SendMembershipReminder(ctx, &fullUser, tier, st); err != nil {
			log.Printf("  ⚠ Failed to queue %s for %s: %v", tier, user.Email, err)
			run.Errors++
			continue
		}
		log.Printf("  ✉ Queued %s for %s (balance: %.0f Kč, paid through %s)", tier, user.Email, st.Balance, st.PaidThrough())
		run.Queued++
	}

	verb := "queued"
	if dryRun {
		verb = "would be queued"
	}
	log.Printf("Members on a paid level: %d, no reminder due: %d, %s: %d, already reminded this month: %d, errors: %d",
		run.Members, run.NoDebt, verb, run.Queued, run.Skipped, run.Errors)
	if dryRun {
		return run, nil
	}

	level := "success"
	if run.Errors > 0 {
		level = "warning"
	}
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "cron",
		Level:     level,
		Message:   fmt.Sprintf("Membership reminders: %d queued, %d skipped, %d errors", run.Queued, run.Skipped, run.Errors),
		Metadata: sql.NullString{String: fmt.Sprintf(`{"queued":%d,"skipped":%d,"no_debt":%d,"errors":%d}`,
			run.Queued, run.Skipped, run.NoDebt, run.Errors), Valid: true},
	})

	if err := recordMonth(ctx, queries, settingRemindersMonth, now); err != nil {
		return run, err
	}
	return run, nil
}
