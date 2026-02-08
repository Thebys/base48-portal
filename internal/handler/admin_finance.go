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

	// Fetch all accepted users with level info
	users, err := h.queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}

	// === Section 1: Summary cards + Section 2: Fee preview ===
	var (
		totalMembers      int
		totalMonthlyFees  int64
		membersInDebt     int
		totalDebt         int64 // sum of absolute values of negative balances
		deepDebt          int64 // debt from members owing more than 1 month's fee

		feePreview         []FeePreviewItem
		previewTotalFees   int64
		previewNegBalance  int
		previewDebtWarning int
	)

	// Determine which period the next fee run would target
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
			// Current month fee doesn't exist yet — preview for current month
			previewPeriod = currentPeriod
		}
	}

	totalMembers = len(users)

	for _, user := range users {
		// Determine fee amount
		feeAmount := user.LevelActualAmount
		if feeAmount == "0" || feeAmount == "" {
			feeAmount = user.LevelAmount
		}

		var feeFloat float64
		fmt.Sscanf(feeAmount, "%f", &feeFloat)
		totalMonthlyFees += int64(feeFloat)

		// Get balance
		balance, err := h.queries.GetUserBalance(ctx, db.GetUserBalanceParams{
			UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
			UserID_2: user.ID,
		})
		if err != nil {
			continue
		}

		if balance < 0 {
			membersInDebt++
			totalDebt += -balance
			if float64(balance) <= -feeFloat {
				deepDebt += -balance
			}
		}

		// Fee preview: balance after hypothetical fee
		balanceAfterFee := balance - int64(feeFloat)
		balanceAfterFloat := float64(balanceAfterFee)

		emailTier := ""
		if balanceAfterFloat <= -(2 * feeFloat) {
			emailTier = "debt_warning"
			previewDebtWarning++
		} else if balanceAfterFloat <= -feeFloat {
			emailTier = "negative_balance"
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

	// Sort by balance after fee (worst debt first)
	sort.Slice(feePreview, func(i, j int) bool {
		return feePreview[i].BalanceAfterFee < feePreview[j].BalanceAfterFee
	})

	// === Section 3: Monthly overview ===
	paymentStats, _ := h.queries.ListMonthlyPaymentStats(ctx)
	feeStats, _ := h.queries.ListMonthlyFeeStats(ctx)

	// Build payment map
	paymentMap := make(map[string]db.ListMonthlyPaymentStatsRow)
	for _, ps := range paymentStats {
		paymentMap[ps.Period] = ps
	}

	// Build fee map
	feeMap := make(map[string]db.ListMonthlyFeeStatsRow)
	for _, fs := range feeStats {
		feeMap[fs.Period] = fs
	}

	// Collect all unique months
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

	// Prediction for next month (always next month — current month data is visible in the rows)
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

	// === Section 4: Unmatched payments ===
	unmatched, _ := h.queries.ListUnassignedPayments(ctx)
	var unmatchedPayments []UnmatchedPaymentItem
	for _, p := range unmatched {
		var amount float64
		fmt.Sscanf(p.Amount, "%f", &amount)
		if amount < 5 {
			continue
		}
		// Skip payments whose VS belongs to a fundraising project
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

	// === "Received this month" from existing payment stats ===
	currentMonthKey := now.Format("2006-01")
	var receivedThisMonth int64
	var receivedThisMonthCount int64
	if ps, ok := paymentMap[currentMonthKey]; ok {
		receivedThisMonth = ps.PaymentTotal
		receivedThisMonthCount = ps.PaymentCount
	}

	// === FIO cached data (bank balance + rent) ===
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
			prevMonthCutoff := currentMonthStart.AddDate(0, 0, -5)
			rentPaid = !rd.Before(prevMonthCutoff)
		}
	}

	daysRemaining := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day() - now.Day()

	data := map[string]interface{}{
		"User": h.auth.GetUser(r),

		// Summary cards
		"TotalMembers":            totalMembers,
		"TotalMonthlyFees":        totalMonthlyFees,
		"ReceivedThisMonth":       receivedThisMonth,
		"ReceivedThisMonthCount":  receivedThisMonthCount,
		"BankBalance":             bankBalance,
		"BankBalanceDate":         bankBalanceDate,
		"RentAmount":              rentAmount,
		"RentDate":                rentDate,
		"RentPaid":                rentPaid,
		"DaysRemaining":           daysRemaining,
		"MembersInDebt":           membersInDebt,
		"TotalDebt":               totalDebt,
		"DeepDebt":                deepDebt,

		// Fee preview
		"FeePreview":         feePreview,
		"PreviewPeriod":      previewPeriod.Format("2006-01"),
		"PreviewTotalFees":   previewTotalFees,
		"PreviewNegBalance":  previewNegBalance,
		"PreviewDebtWarning": previewDebtWarning,

		// Monthly overview
		"MonthlyRows": monthlyRows,
		"Prediction":  prediction,

		// Unmatched payments
		"UnmatchedCount":    unmatchedCount,
		"UnmatchedPayments": unmatchedPayments,
	}

	h.render(w, "admin_finance.html", data)
}
