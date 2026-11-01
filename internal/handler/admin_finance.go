package handler

import (
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/base48/member-portal/internal/dues"
	"github.com/base48/member-portal/internal/email"
	"github.com/base48/member-portal/internal/unmatched"
)

type FeePreviewItem struct {
	UserID          int64
	Username        string
	Email           string
	Realname        string
	Balance         int64
	FeeAmount       string
	BalanceAfterFee int64
	EmailTier       string // a membership reminder, or "" for none
}

type UnmatchedPaymentItem struct {
	ID             int64
	Date           time.Time
	Amount         string
	Identification string
	RemoteAccount  string
}

// MonthlyRow is a month of the finance table, in whole crowns.
type MonthlyRow struct {
	Period       string
	PaymentCount int64
	PaymentTotal int64
	FeeCount     int64
	FeeTotal     int64
	Delta        int64
	Debt         int64 // owed at the month's end by those charged for it
	Debtors      int
	Balance      int64 // on the accounts at the month's end
	HasBalance   bool
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

	// The month the next run charges: this one until it has been charged,
	// then the next.
	now := time.Now()
	thisMonth := dues.MonthOf(now)
	previewMonth := thisMonth
	if run, err := dues.RunMonth(ctx, h.queries, now); err == nil && !run.Before(thisMonth) {
		previewMonth = thisMonth.Add(1)
	}

	standings, err := dues.LoadAll(ctx, h.queries, users, now)
	if err != nil {
		http.Error(w, "Failed to load balances", http.StatusInternalServerError)
		return
	}
	policy := email.LoadMembershipSettings(ctx, h.queries).Policy()

	var (
		feePreview         []FeePreviewItem
		previewTotalFees   int64
		previewPrepaid     int
		previewNegBalance  int
		previewDebtWarning int
	)

	for _, user := range users {
		st := standings[user.ID]
		after := st.AfterCharge(previewMonth)
		emailTier := dues.Classify(after, previewMonth, policy)
		switch emailTier {
		case email.TierDebtWarning:
			previewDebtWarning++
		case email.TierNegativeBalance:
			previewNegBalance++
		case email.TierPrepaidEnding:
			previewPrepaid++
		}

		feeAmount, _, err := dues.FeeAmount(user.LevelActualAmount, user.LevelAmount)
		if err != nil {
			feeAmount = "?"
		}
		previewTotalFees += int64(st.Fee)

		feePreview = append(feePreview, FeePreviewItem{
			UserID:          user.ID,
			Username:        user.Username.String,
			Email:           user.Email,
			Realname:        user.Realname.String,
			Balance:         int64(st.Balance),
			FeeAmount:       feeAmount,
			BalanceAfterFee: int64(after.Balance),
			EmailTier:       emailTier,
		})
	}

	sort.Slice(feePreview, func(i, j int) bool {
		return feePreview[i].BalanceAfterFee < feePreview[j].BalanceAfterFee
	})

	// === Admin-only: Unmatched payments ===
	pending, err := unmatched.List(ctx, h.queries)
	if err != nil {
		log.Printf("[Finance] list unmatched payments: %v", err)
	}
	var unmatchedPayments []UnmatchedPaymentItem
	for _, p := range pending {
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
		"Title":   "Finanční přehled",
		"User":    h.auth.GetUser(r),
		"Finance": summary,

		// Fee preview (admin-only)
		"FeePreview":         feePreview,
		"PreviewPeriod":      previewMonth.String(),
		"PreviewTotalFees":   previewTotalFees,
		"PreviewPrepaid":     previewPrepaid,
		"PrepaidEnabled":     policy.Prepaid,
		"PreviewNegBalance":  previewNegBalance,
		"PreviewDebtWarning": previewDebtWarning,

		// Unmatched payments (admin-only)
		"UnmatchedCount":    unmatchedCount,
		"UnmatchedPayments": unmatchedPayments,
	}

	h.render(w, "admin_finance.html", data)
}
