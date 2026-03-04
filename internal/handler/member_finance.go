package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/base48/member-portal/internal/db"
)

// financeSummary holds aggregate financial data safe for all authenticated users.
type financeSummary struct {
	TotalMembers           int
	TotalMonthlyFees       int64
	ReceivedThisMonth      int64
	ReceivedThisMonthCount int64
	BankBalance            int64
	BankBalanceDate        string
	RentAmount             int64
	RentDate               string
	RentPaid               bool
	DaysRemaining          int
	MembersInDebt          int
	TotalDebt              int64
	DeepDebt               int64
	MonthlyRows            []MonthlyRow
	Prediction             MonthlyRow
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

	// Monthly overview
	paymentStats, _ := h.queries.ListMonthlyPaymentStats(ctx)
	feeStats, _ := h.queries.ListMonthlyFeeStats(ctx)

	paymentMap := make(map[string]db.ListMonthlyPaymentStatsRow)
	for _, ps := range paymentStats {
		paymentMap[ps.Period] = ps
	}

	feeMap := make(map[string]db.ListMonthlyFeeStatsRow)
	for _, fs := range feeStats {
		feeMap[fs.Period] = fs
	}

	monthSet := make(map[string]bool)
	for k := range paymentMap {
		monthSet[k] = true
	}
	for k := range feeMap {
		monthSet[k] = true
	}

	var months []string
	for m := range monthSet {
		months = append(months, m)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))

	var monthlyRows []MonthlyRow
	var recentPaymentTotals []int64

	for _, m := range months {
		ps := paymentMap[m]
		fs := feeMap[m]
		row := MonthlyRow{
			Period:       m,
			PaymentCount: ps.PaymentCount,
			PaymentTotal: ps.PaymentTotal,
			FeeCount:     fs.FeeCount,
			FeeTotal:     fs.FeeTotal,
			Delta:        ps.PaymentTotal - fs.FeeTotal,
		}
		monthlyRows = append(monthlyRows, row)

		if len(recentPaymentTotals) < 3 && ps.PaymentTotal > 0 {
			recentPaymentTotals = append(recentPaymentTotals, ps.PaymentTotal)
		}
	}

	var avgPayments int64
	if len(recentPaymentTotals) > 0 {
		var sum int64
		for _, v := range recentPaymentTotals {
			sum += v
		}
		avgPayments = sum / int64(len(recentPaymentTotals))
	}

	prediction := MonthlyRow{
		Period:       nextPeriod.Format("2006-01"),
		PaymentCount: 0,
		PaymentTotal: avgPayments,
		FeeCount:     int64(totalMembers),
		FeeTotal:     totalMonthlyFees,
		Delta:        avgPayments - totalMonthlyFees,
		IsPrediction: true,
	}

	// Received this month
	currentMonthKey := now.Format("2006-01")
	var receivedThisMonth int64
	var receivedThisMonthCount int64
	if ps, ok := paymentMap[currentMonthKey]; ok {
		receivedThisMonth = ps.PaymentTotal
		receivedThisMonthCount = ps.PaymentCount
	}

	// FIO cached data
	var bankBalance int64
	var bankBalanceDate string
	if setting, err := h.queries.GetSetting(ctx, "fio_closing_balance"); err == nil {
		fmt.Sscanf(setting.Value, "%d", &bankBalance)
	}
	if setting, err := h.queries.GetSetting(ctx, "fio_closing_balance_date"); err == nil {
		bankBalanceDate = setting.Value
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
		BankBalance:            bankBalance,
		BankBalanceDate:        bankBalanceDate,
		RentAmount:             rentAmount,
		RentDate:               rentDate,
		RentPaid:               rentPaid,
		DaysRemaining:          daysRemaining,
		MembersInDebt:          membersInDebt,
		TotalDebt:              totalDebt,
		DeepDebt:               deepDebt,
		MonthlyRows:            monthlyRows,
		Prediction:             prediction,
	}, nil
}

// MemberFinanceHandler shows the financial overview for authenticated members (read-only, aggregate data only).
// GET /finance
func (h *Handler) MemberFinanceHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return
	}

	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Awaiting members don't have access to financial overview
	if dbUser.State == "awaiting" {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
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

		"TotalMembers":           summary.TotalMembers,
		"TotalMonthlyFees":       summary.TotalMonthlyFees,
		"ReceivedThisMonth":      summary.ReceivedThisMonth,
		"ReceivedThisMonthCount": summary.ReceivedThisMonthCount,
		"BankBalance":            summary.BankBalance,
		"BankBalanceDate":        summary.BankBalanceDate,
		"RentAmount":             summary.RentAmount,
		"RentDate":               summary.RentDate,
		"RentPaid":               summary.RentPaid,
		"DaysRemaining":          summary.DaysRemaining,
		"MembersInDebt":          summary.MembersInDebt,
		"TotalDebt":              summary.TotalDebt,
		"DeepDebt":               summary.DeepDebt,
		"MonthlyRows":            summary.MonthlyRows,
		"Prediction":             summary.Prediction,
	}

	h.render(w, "member_finance.html", data)
}
