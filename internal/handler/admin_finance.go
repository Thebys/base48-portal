package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/base48/member-portal/internal/db"
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

	for _, user := range users {
		feeAmount := user.LevelActualAmount
		if feeAmount == "0" || feeAmount == "" {
			feeAmount = user.LevelAmount
		}

		var feeFloat float64
		fmt.Sscanf(feeAmount, "%f", &feeFloat)

		balance, err := h.queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
			UserID_2: user.ID,
		})
		if err != nil {
			continue
		}

		balanceAfterFee := balance - int64(feeFloat)
		balanceAfterFloat := float64(balanceAfterFee)

		emailTier := ""
		if feeFloat > 0 {
			if balanceAfterFloat <= -(2 * feeFloat) {
				emailTier = "debt_warning"
				previewDebtWarning++
			} else if balanceAfterFloat <= -feeFloat {
				emailTier = "negative_balance"
				previewNegBalance++
			}
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

		// Monthly overview (from shared helper)
		"MonthlyRows": summary.MonthlyRows,
		"Prediction":  summary.Prediction,

		// Unmatched payments (admin-only)
		"UnmatchedCount":    unmatchedCount,
		"UnmatchedPayments": unmatchedPayments,
	}

	h.render(w, "admin_finance.html", data)
}
