package handler

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
)

type FeePreviewItem struct {
	UserID          int64
	Username        string
	Email           string
	Realname        string
	Balance         int64
	FeeAmount       string
	BalanceAfterFee int64
	EmailTier       string // "", "negative_balance", "debt_warning"
}

type UnmatchedPaymentItem struct {
	ID             int64
	Date           time.Time
	Amount         string
	Identification string
	RemoteAccount  string
}

type MonthlyRow struct {
	Period       string
	PaymentCount int64
	PaymentTotal int64
	FeeCount     int64
	FeeTotal     int64
	Delta        int64
	IsPrediction bool
}

func (h *Handler) AdminFinanceHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Shared summary (tiles + monthly overview)
	summary, err := h.buildFinanceSummary(ctx)
	if err != nil {
		http.Error(w, "Failed to load financial data", http.StatusInternalServerError)
		return
	}

	// === Admin-only: Fee preview ===
	users, err := h.queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	currentPeriod := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextPeriod := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	previewPeriod := nextPeriod
	if len(users) > 0 {
		_, err := h.queries.GetFeeByUserAndPeriod(ctx, db.GetFeeByUserAndPeriodParams{
			UserID:      users[0].ID,
			PeriodStart: currentPeriod,
		})
		if err != nil {
			previewPeriod = currentPeriod
		}
	}

	var (
		feePreview         []FeePreviewItem
		previewTotalFees   int64
		previewNegBalance  int
		previewDebtWarning int
	)

	// Batch-fetch all user balances in one query (avoids N+1)
	balanceMap, err := h.getUserBalanceMap(ctx)
	if err != nil {
		http.Error(w, "Failed to load balances", http.StatusInternalServerError)
		return
	}

	for _, user := range users {
		feeAmount := user.LevelActualAmount
		if feeAmount == "0" || feeAmount == "" {
			feeAmount = user.LevelAmount
		}

		feeFloat := monthlyFeeOf(user.LevelActualAmount, user.LevelAmount)

		balance := balanceMap[user.ID]

		balanceAfterFee := balance - int64(feeFloat)

		emailTier := email.DebtTier(float64(balanceAfterFee), feeFloat)
		switch emailTier {
		case email.TierDebtWarning:
			previewDebtWarning++
		case email.TierNegativeBalance:
			previewNegBalance++
		}

		previewTotalFees += int64(feeFloat)

		realname := ""
		if user.Realname.Valid {
			realname = user.Realname.String
		}
		username := ""
		if user.Username.Valid {
			username = user.Username.String
		}

		feePreview = append(feePreview, FeePreviewItem{
			UserID:          user.ID,
			Username:        username,
			Email:           user.Email,
			Realname:        realname,
			Balance:         balance,
			FeeAmount:       feeAmount,
			BalanceAfterFee: balanceAfterFee,
			EmailTier:       emailTier,
		})
	}

	sort.Slice(feePreview, func(i, j int) bool {
		return feePreview[i].BalanceAfterFee < feePreview[j].BalanceAfterFee
	})

	// === Admin-only: manual debt reminder picker ===
	// Separate from the fee-run preview above: that one projects balances after
	// the upcoming fee, while a reminder quotes what the member owes right now.
	debtCandidates, err := h.buildDebtCandidates(ctx)
	if err != nil {
		http.Error(w, "Failed to load debt candidates", http.StatusInternalServerError)
		return
	}
	debtSendable := 0
	for _, c := range debtCandidates {
		if !c.Blocked {
			debtSendable++
		}
	}

	// === Admin-only: Unmatched payments ===
	unmatched, _ := h.queries.ListUnassignedPayments(ctx)
	var unmatchedPayments []UnmatchedPaymentItem
	for _, p := range unmatched {
		var amount float64
		fmt.Sscanf(p.Amount, "%f", &amount)
		if amount < 5 {
			continue
		}
		if p.Identification != "" {
			if _, err := h.queries.GetProjectByPaymentsID(ctx, p.Identification); err == nil {
				continue
			}
		}
		unmatchedPayments = append(unmatchedPayments, UnmatchedPaymentItem{
			ID:             p.ID,
			Date:           p.Date,
			Amount:         p.Amount,
			Identification: p.Identification,
			RemoteAccount:  p.RemoteAccount,
		})
	}
	unmatchedCount := len(unmatchedPayments)

	data := map[string]interface{}{
		"User": h.auth.GetUser(r),

		// Summary cards (from shared helper)
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

		// Fee preview (admin-only)
		"FeePreview":         feePreview,
		"PreviewPeriod":      previewPeriod.Format("2006-01"),
		"PreviewTotalFees":   previewTotalFees,
		"PreviewNegBalance":  previewNegBalance,
		"PreviewDebtWarning": previewDebtWarning,

		// Manual debt reminders (admin-only)
		"DebtCandidates": debtCandidates,
		"DebtSendable":   debtSendable,
		"EmailEnabled":   h.config.EmailEnabled,

		// Monthly overview (from shared helper)
		"MonthlyRows": summary.MonthlyRows,
		"Prediction":  summary.Prediction,

		// Unmatched payments (admin-only)
		"UnmatchedCount":    unmatchedCount,
		"UnmatchedPayments": unmatchedPayments,
	}

	h.render(w, "admin_finance.html", data)
}
