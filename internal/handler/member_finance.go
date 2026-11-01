package handler

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/base48/member-portal/internal/access"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/finance"
)

// financeSummary holds aggregate financial data safe for all authenticated users.
type financeSummary struct {
	TotalMembers           int
	TotalMonthlyFees       int64
	ReceivedThisMonth      int64
	ReceivedThisMonthCount int64
	BankBalance            int64  // on all accounts
	BankBalanceDate        string // the latest day a balance is known for
	BankAccounts           []db.ListLatestBalancesRow
	RentAmount             int64
	RentDate               string
	RentPaid               bool
	DaysRemaining          int
	MembersInDebt          int
	TotalDebt              int64
	DeepDebt               int64
	MonthlyRows            []MonthlyRow
	Prediction             MonthlyRow
	Chart                  []finance.Month // the closed months, for the chart
}

// buildFinanceSummary computes aggregate financial data: summary tiles and monthly overview.
// This is used by both the member and admin finance pages.
func (h *Handler) buildFinanceSummary(ctx context.Context) (*financeSummary, error) {
	users, err := h.queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load users: %w", err)
	}

	now := time.Now()
	nextPeriod := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)

	var (
		totalMembers     int
		totalMonthlyFees int64
		membersInDebt    int
		totalDebt        int64
		deepDebt         int64
	)

	totalMembers = len(users)

	// Batch-fetch all user balances in one query (avoids N+1)
	balanceMap, err := h.getUserBalanceMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load balances: %w", err)
	}

	for _, user := range users {
		feeAmount := user.LevelActualAmount
		if feeAmount == "0" || feeAmount == "" {
			feeAmount = user.LevelAmount
		}

		var feeFloat float64
		fmt.Sscanf(feeAmount, "%f", &feeFloat)
		totalMonthlyFees += int64(feeFloat)

		balance := balanceMap[user.ID]

		if balance < 0 {
			membersInDebt++
			totalDebt += -balance
			if float64(balance) <= -feeFloat {
				deepDebt += -balance
			}
		}
	}

	// Month by month since the first fee. The table shows the last 13 months
	// and the current one; the chart stops at the last closed month, since a
	// running one has half its payments and all its fees.
	history, err := h.financeHistory(ctx, now)
	if err != nil {
		return nil, err
	}
	var monthlyRows []MonthlyRow
	for i := len(history) - 1; i >= 0 && len(monthlyRows) < 14; i-- {
		monthlyRows = append(monthlyRows, monthlyRow(history[i]))
	}

	// The prediction expects what the last three whole months brought.
	var recent []int64
	for i := len(history) - 2; i >= 0 && len(recent) < 3; i-- {
		if history[i].Paid > 0 {
			recent = append(recent, int64(history[i].Paid))
		}
	}
	var avgPayments int64
	for _, v := range recent {
		avgPayments += v
	}
	if len(recent) > 0 {
		avgPayments /= int64(len(recent))
	}
	prediction := MonthlyRow{
		Period:       nextPeriod.Format("2006-01"),
		PaymentTotal: avgPayments,
		FeeCount:     int64(totalMembers),
		FeeTotal:     totalMonthlyFees,
		Delta:        avgPayments - totalMonthlyFees,
		IsPrediction: true,
	}

	var receivedThisMonth, receivedThisMonthCount int64
	if n := len(history); n > 0 {
		receivedThisMonth = int64(history[n-1].Paid)
		receivedThisMonthCount = history[n-1].Payments
	}

	// What the accounts hold now: each one as the bank last reported it.
	latest, err := h.queries.ListLatestBalances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load account balances: %w", err)
	}
	var bankBalance float64
	var bankBalanceDate string
	for _, b := range latest {
		bankBalance += b.Balance
		bankBalanceDate = max(bankBalanceDate, b.Day)
	}

	var rentAmount int64
	var rentDate string
	var rentPaid bool
	if setting, err := h.queries.GetSetting(ctx, "fio_last_rent_date"); err == nil {
		rentDate = setting.Value
	}
	if setting, err := h.queries.GetSetting(ctx, "fio_last_rent_amount"); err == nil {
		fmt.Sscanf(setting.Value, "%d", &rentAmount)
	}
	if rentDate != "" {
		if rd, err := time.Parse("2006-01-02", rentDate); err == nil {
			currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			rentPaid = !rd.Before(currentMonthStart)
		}
	}

	daysRemaining := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day() - now.Day()

	return &financeSummary{
		TotalMembers:           totalMembers,
		TotalMonthlyFees:       totalMonthlyFees,
		ReceivedThisMonth:      receivedThisMonth,
		ReceivedThisMonthCount: receivedThisMonthCount,
		BankBalance:            int64(math.Round(bankBalance)),
		BankBalanceDate:        bankBalanceDate,
		BankAccounts:           latest,
		RentAmount:             rentAmount,
		RentDate:               rentDate,
		RentPaid:               rentPaid,
		DaysRemaining:          daysRemaining,
		MembersInDebt:          membersInDebt,
		TotalDebt:              totalDebt,
		DeepDebt:               deepDebt,
		MonthlyRows:            monthlyRows,
		Prediction:             prediction,
		Chart:                  history[:max(0, len(history)-1)],
	}, nil
}

// MemberFinanceHandler shows the financial overview for authenticated members (read-only, aggregate data only).
// GET /finance
func (h *Handler) MemberFinanceHandler(w http.ResponseWriter, r *http.Request) {
	user, dbUser, acc, ok := h.memberPage(w, r, access.Subject.MemberArea)
	if !ok {
		return
	}

	summary, err := h.buildFinanceSummary(r.Context())
	if err != nil {
		http.Error(w, "Failed to load financial data", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":  "Finanční přehled",
		"User":   user,
		"DBUser": dbUser,
		"Access": acc,

		"Finance": summary,
	}

	h.render(w, "member_finance.html", data)
}

// financeHistory is the association's history month by month until now.
func (h *Handler) financeHistory(ctx context.Context, now time.Time) ([]finance.Month, error) {
	fees, err := h.queries.ListMonthlyFeesByUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load fees: %w", err)
	}
	payments, err := h.queries.ListMonthlyMemberPaymentsByUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load payments: %w", err)
	}
	balances, err := h.queries.ListBankBalances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load account balances: %w", err)
	}
	return finance.History(fees, payments, balances, now), nil
}

// monthlyRow is a month of the history as the table shows it, in whole crowns.
func monthlyRow(m finance.Month) MonthlyRow {
	row := MonthlyRow{
		Period:       m.Month,
		PaymentCount: m.Payments,
		PaymentTotal: int64(math.Round(m.Paid)),
		FeeCount:     m.Fees,
		FeeTotal:     int64(math.Round(m.Charged)),
		Debt:         int64(math.Round(m.Debt)),
		Debtors:      m.Debtors,
	}
	row.Delta = row.PaymentTotal - row.FeeTotal
	if m.Balance != nil {
		row.Balance = int64(math.Round(m.Balance.End))
		row.HasBalance = true
	}
	return row
}
