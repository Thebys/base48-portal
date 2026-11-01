package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/fio"
)

const balancesUsage = `Usage:
  portal-cron fio-balances - < file     load balances from a statement saved from FIO

Loads what an account held day by day, for the balance chart on the finance
page. Any of the association's accounts will do, the old transparent one
too: payments are not touched. The sync keeps the last 85 days of the
payment account current by itself.
`

// runFIOBalances loads the day-end balances of a statement read from stdin.
func runFIOBalances(ctx context.Context, queries *db.Queries, args []string, stdin io.Reader) int {
	if len(args) != 1 || args[0] != "-" {
		fmt.Fprint(os.Stderr, balancesUsage)
		return 2
	}
	statement, err := fio.ParseStatement(stdin)
	if err != nil {
		log.Printf("✗ statement on stdin: %v", err)
		return 1
	}
	account, err := statementAccount(statement)
	if err != nil {
		log.Printf("✗ %v", err)
		return 1
	}
	days, err := recordBalances(ctx, queries, account, statement)
	if err != nil {
		log.Printf("✗ %s, %s to %s: %v", account, statement.DateStart, statement.DateEnd, err)
		return 1
	}
	log.Printf("✓ %s, %s to %s: %d day balances, closing %.2f CZK",
		account, statement.DateStart, statement.DateEnd, days, statement.ClosingBalance)
	queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "fio_sync",
		Level:     "info",
		Message: fmt.Sprintf("FIO balances %s %s–%s: %d days loaded",
			account, statement.DateStart, statement.DateEnd, days),
	})
	return 0
}

// statementAccount names the account a statement is of the way payments
// name it, 2003436319/2010, once the header agrees with itself.
func statementAccount(res *fio.FetchResult) (string, error) {
	acc := bank.Account{IBAN: res.Account.IBAN, Number: res.Account.AccountID, BankCode: res.Account.BankID}
	if err := acc.Validate(); err != nil {
		return "", fmt.Errorf("statement header: %w", err)
	}
	return acc.Domestic(), nil
}

// recordBalances stores what the statement says the account held at the end
// of each day, and returns how many days that was.
func recordBalances(ctx context.Context, queries *db.Queries, account string, res *fio.FetchResult) (int, error) {
	days, err := res.DailyBalances()
	if err != nil {
		return 0, err
	}
	for _, d := range days {
		if err := queries.UpsertBankBalance(ctx, db.UpsertBankBalanceParams{
			Account: account, Day: d.Day, Balance: d.Balance,
		}); err != nil {
			return 0, err
		}
	}
	return len(days), nil
}
