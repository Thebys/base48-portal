package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/joho/godotenv"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/fio"
)

// Test script to verify FIO API connectivity and fetch recent transactions
//
// Usage:
//   go run cmd/test/test_fio_api.go

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Check FIO token
	if cfg.BankFIOToken == "" {
		log.Fatal("BANK_FIO_TOKEN is required in .env file")
	}

	log.Println("✓ FIO token loaded")

	// Create FIO API client
	fioClient := fio.NewClient(cfg.BankFIOToken)
	ctx := context.Background()

	// Fetch last 7 days of transactions as a test
	dateFrom := time.Now().AddDate(0, 0, -7)
	dateTo := time.Now()

	log.Printf("Fetching transactions from %s to %s...",
		fio.FormatDate(dateFrom), fio.FormatDate(dateTo))

	result, err := fioClient.FetchTransactionsByPeriod(
		ctx,
		fio.FormatDate(dateFrom),
		fio.FormatDate(dateTo),
	)

	if err != nil {
		log.Fatalf("Failed to fetch transactions: %v", err)
	}

	transactions := result.Transactions
	log.Printf("\n✓ Successfully fetched %d transactions (closing balance: %.0f)\n", len(transactions), result.ClosingBalance)

	if len(transactions) == 0 {
		log.Println("No transactions found in the last 7 days")
		return
	}

	// Display transactions
	fmt.Println("\nRecent transactions:")
	fmt.Println(repeat("-", 100))
	fmt.Printf("%-12s %-12s %-30s %-10s %-15s %s\n",
		"Date", "Amount", "Account", "VS", "Bank", "Message")
	fmt.Println(repeat("-", 100))

	for _, tx := range transactions {
		accountDisplay := tx.AccountName
		if accountDisplay == "" {
			accountDisplay = tx.AccountNumber
		}
		if len(accountDisplay) > 28 {
			accountDisplay = accountDisplay[:28] + ".."
		}

		message := tx.Message
		if message == "" {
			message = tx.Comment
		}
		if len(message) > 25 {
			message = message[:25] + "..."
		}

		fmt.Printf("%-12s %10.2f CZK %-30s %-10s %-15s %s\n",
			tx.Date[:10], // Only date part
			tx.Amount,
			accountDisplay,
			tx.VariableSymbol,
			tx.BankName,
			message,
		)
	}

	fmt.Println(repeat("-", 100))
	fmt.Printf("\nTotal: %d transactions\n", len(transactions))
}

func repeat(s string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += s
	}
	return result
}
