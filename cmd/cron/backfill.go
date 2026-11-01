package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/fio"
)

const backfillUsage = `Usage:
  portal-cron fio-backfill YYYY-MM-DD   read FIO from that date until today
  portal-cron fio-backfill - < file     read a statement saved from FIO

Records outgoing payments the regular sync, which reads 85 days back, has
not seen. Incoming payments it only lists when missing: they change members'
balances, so they are left to the sync or to an admin.

FIO serves more than 90 days back only within 10 minutes of the account
owner allowing it in internet banking, and one request per token every
30 seconds.
`

// runFIOBackfill records outgoing payments older than the regular sync
// reaches. stdin is where "-" reads the statement from.
func runFIOBackfill(ctx context.Context, cfg *config.Config, queries *db.Queries, args []string, stdin io.Reader) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, backfillUsage)
		return 2
	}

	statement, err := readBackfillStatement(ctx, cfg, args[0], stdin)
	if err != nil {
		log.Printf("✗ %v", err)
		return 1
	}

	// Only the payment account's own statement: another account's (the old
	// transparent one, say) would be booked as this account's spending.
	account, known := bank.Load(ctx, queries)
	if !known {
		log.Printf("✗ %v", bank.ErrUnknown)
		return 1
	}
	if got := strings.ReplaceAll(strings.ToUpper(statement.Account.IBAN), " ", ""); got != account.IBAN {
		log.Printf("✗ The statement is of %s/%s (%s), the payment account is %s (%s); not importing",
			statement.Account.AccountID, statement.Account.BankID, got, account.Domestic(), account.IBAN)
		return 1
	}

	var recorded, already, failed int
	var missingIncoming []fio.Transaction
	for _, tx := range statement.Transactions {
		switch {
		case tx.Amount < 0:
			switch ok, err := recordOutgoing(ctx, queries, tx, account.Domestic()); {
			case err != nil:
				log.Printf("✗ FIO ID %d: %v", tx.ID, err)
				failed++
			case ok:
				recorded++
			default:
				already++
			}
		case tx.Amount > 0:
			if _, err := queries.GetPaymentByKindAndID(ctx, db.GetPaymentByKindAndIDParams{
				Kind: "fio", KindID: strconv.FormatInt(tx.ID, 10),
			}); err != nil {
				missingIncoming = append(missingIncoming, tx)
			}
		}
	}

	log.Printf("Statement %s to %s: %d transactions", statement.DateStart, statement.DateEnd, len(statement.Transactions))
	log.Printf("  ↗ Outgoing recorded: %d", recorded)
	log.Printf("  - Outgoing already there: %d", already)
	log.Printf("  ✗ Errors: %d", failed)
	if len(missingIncoming) > 0 {
		log.Printf("\n⚠ %d incoming payments are not in the portal (not imported here):", len(missingIncoming))
		for _, tx := range missingIncoming {
			log.Printf("   %s  %10.2f CZK  VS %-10s  FIO ID %d", tx.Date, tx.Amount, tx.VariableSymbol, tx.ID)
		}
	}

	level := "info"
	if failed > 0 {
		level = "warning"
	}
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "fio_sync",
		Level:     level,
		Message: fmt.Sprintf("FIO backfill %s–%s: %d outgoing recorded, %d already there, %d incoming missing",
			statement.DateStart, statement.DateEnd, recorded, already, len(missingIncoming)),
	})
	if failed > 0 {
		return 1
	}
	return 0
}

// readBackfillStatement reads the statement from stdin for "-", or fetches
// it from FIO from the given date until today.
func readBackfillStatement(ctx context.Context, cfg *config.Config, arg string, stdin io.Reader) (*fio.FetchResult, error) {
	if arg == "-" {
		res, err := fio.ParseStatement(stdin)
		if err != nil {
			return nil, fmt.Errorf("statement on stdin: %w", err)
		}
		return res, nil
	}

	from, err := time.Parse("2006-01-02", arg)
	if err != nil {
		return nil, fmt.Errorf("%q is not a date (YYYY-MM-DD)", arg)
	}
	if cfg.BankFIOToken == "" {
		return nil, errors.New("BANK_FIO_TOKEN is not set")
	}
	res, err := fio.NewClient(cfg.BankFIOToken).FetchTransactionsByPeriod(ctx, fio.FormatDate(from), fio.FormatDate(time.Now()))
	var apiErr *fio.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict:
		return nil, errors.New("FIO allows one request per 30 seconds and the sync has just used it; try again in a minute")
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity:
		return nil, errors.New("FIO refused data older than 90 days: allow the history in internet banking first, then run this within 10 minutes")
	}
	return res, err
}
