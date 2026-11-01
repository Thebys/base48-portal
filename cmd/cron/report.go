package main

import (
	"context"
	"fmt"
	"log"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/unmatched"
)

// runReport prints the incoming payments the sync could not assign, sorted
// the same way as /admin/payments/unmatched.
//
// Usage:
//
//	portal-cron report
func runReport(ctx context.Context, _ *config.Config, queries *db.Queries) int {
	payments, err := unmatched.List(ctx, queries)
	if err != nil {
		log.Printf("✗ Failed to list unmatched payments: %v", err)
		return 1
	}

	byCategory := map[unmatched.Category][]unmatched.Payment{}
	total := 0.0
	for _, p := range payments {
		byCategory[p.Category] = append(byCategory[p.Category], p)
		total += p.Value
	}

	fmt.Println("\n" + repeat("=", 120))
	fmt.Println("UNMATCHED PAYMENTS REPORT")
	fmt.Println(repeat("=", 120))
	fmt.Printf("\nUnassigned incoming payments of %.0f CZK or more: %d, %.2f CZK in total\n",
		unmatched.MinAmount, len(payments), total)

	if len(payments) == 0 {
		fmt.Println("✓ No problematic payments found!")
		return 0
	}

	for _, section := range []struct {
		category unmatched.Category
		title    string
	}{
		{unmatched.EmptyVS, "📝 PAYMENTS WITH EMPTY VARIABLE SYMBOL:"},
		{unmatched.UserNotFound, "❌ PAYMENTS WITH A VS NO MEMBER HAS:"},
		{unmatched.SyncBug, "🐛 PAYMENTS WITH A MEMBER'S VS BUT NOT ASSIGNED (POTENTIAL SYNC BUG):"},
	} {
		if rows := byCategory[section.category]; len(rows) > 0 {
			fmt.Println("\n" + section.title)
			fmt.Println(repeat("-", 120))
			printPaymentTable(rows)
		}
	}

	fmt.Println("\n" + repeat("=", 120))
	fmt.Println("\n💡 Next steps:")
	fmt.Println("  1. Empty VS or a VS no member has: assign by hand in /admin/payments/unmatched")
	fmt.Println("  2. A member's VS but unassigned: run the sync again or look at the matching")
	fmt.Println()

	return 0
}

func printPaymentTable(payments []unmatched.Payment) {
	fmt.Printf("%-8s %-12s %14s %-30s %s\n", "ID", "Date", "Amount", "Remote Account", "VS")
	fmt.Println(repeat("-", 120))

	for _, p := range payments {
		remoteAcc := p.RemoteAccount
		if len(remoteAcc) > 28 {
			remoteAcc = remoteAcc[:28] + ".."
		}
		fmt.Printf("%-8d %-12s %10s CZK %-30s %s\n",
			p.ID, p.Date.Format("2006-01-02"), p.Amount, remoteAcc, p.Identification)
	}
}
