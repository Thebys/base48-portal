package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
)

// maxManualDelay caps how far ahead a manual reminder may be parked. A week is
// well past the point where the balance it quotes is still current.
const maxManualDelay = 7 * 24 * time.Hour

// recentReminderWindow mirrors the nightly cron's anti-spam window. A member
// reminded inside it is pre-unchecked in the picker, but an admin can still
// override — a manual resend is a deliberate act, unlike the cron's sweep.
const recentReminderWindow = 30 * 24 * time.Hour

// DebtCandidate is one member the admin may remind about a negative balance,
// with everything needed to decide: the current balance (not the fee-run
// projection), which email they would get, and what they were already sent.
type DebtCandidate struct {
	UserID     int64
	Name       string // nickname, else realname, else email
	Email      string
	Locale     string
	Balance    int64
	MonthlyFee int64
	Tier       string // "debt_warning" | "negative_balance"

	LastRemindedAt   time.Time // zero when never reminded
	LastRemindedDays int
	LastTier         string
	HasPending       bool // a reminder is still sitting in the outbox

	// Blocked marks a candidate the picker leaves unchecked, with the reason
	// shown next to them. It is advisory: the send endpoint honours it unless
	// the admin explicitly asks to override.
	Blocked     bool
	BlockReason string
}

// debtReminderHistory folds the outbox down to one entry per member: their most
// recent debt reminder plus whether any is still pending.
type debtReminderHistory struct {
	lastAt     time.Time
	lastTier   string
	hasPending bool
}

func (h *Handler) loadDebtReminderHistory(ctx context.Context) map[int64]debtReminderHistory {
	history := map[int64]debtReminderHistory{}

	rows, err := h.queries.ListRecentDebtEmails(ctx)
	if err != nil {
		log.Printf("[Handler] failed to load debt reminder history: %v", err)
		return history
	}

	// Rows arrive newest first, so the first sighting of a user is their latest.
	for _, row := range rows {
		if !row.UserID.Valid {
			continue
		}
		entry := history[row.UserID.Int64]
		if entry.lastAt.IsZero() {
			entry.lastAt = row.CreatedAt
			entry.lastTier = strings.TrimSuffix(row.TemplateName, ".html")
		}
		if row.Status == "pending" {
			entry.hasPending = true
		}
		history[row.UserID.Int64] = entry
	}

	return history
}

// buildDebtCandidates lists every accepted member whose current balance already
// warrants a reminder. This deliberately uses the balance as it stands, not the
// fee-run preview's balance-after-fee: the email quotes what the member owes now.
func (h *Handler) buildDebtCandidates(ctx context.Context) ([]DebtCandidate, error) {
	users, err := h.queries.ListAcceptedUsersForFees(ctx)
	if err != nil {
		return nil, err
	}

	balances, err := h.getUserBalanceMap(ctx)
	if err != nil {
		return nil, err
	}

	history := h.loadDebtReminderHistory(ctx)
	now := time.Now().UTC()

	var candidates []DebtCandidate
	for _, user := range users {
		monthlyFee := email.MonthlyFee(user.LevelActualAmount, user.LevelAmount)
		balance := balances[user.ID]

		tier := email.DebtTier(float64(balance), monthlyFee)
		if tier == "" {
			continue
		}

		name := user.Email
		if user.Realname.Valid && user.Realname.String != "" {
			name = user.Realname.String
		}
		if user.Username.Valid && user.Username.String != "" {
			name = user.Username.String
		}

		candidate := DebtCandidate{
			UserID:     user.ID,
			Name:       name,
			Email:      user.Email,
			Locale:     user.Locale,
			Balance:    balance,
			MonthlyFee: int64(monthlyFee),
			Tier:       tier,
		}

		past := history[user.ID]
		candidate.HasPending = past.hasPending
		if !past.lastAt.IsZero() {
			candidate.LastRemindedAt = past.lastAt
			candidate.LastRemindedDays = int(now.Sub(past.lastAt).Hours() / 24)
			candidate.LastTier = past.lastTier
		}

		// Pending wins over "recently sent" — it is the more surprising state.
		switch {
		case candidate.HasPending:
			candidate.Blocked = true
			candidate.BlockReason = "už čeká v outboxu"
		case !past.lastAt.IsZero() && now.Sub(past.lastAt) < recentReminderWindow:
			candidate.Blocked = true
			candidate.BlockReason = fmt.Sprintf("posláno před %d dny", candidate.LastRemindedDays)
		}

		candidates = append(candidates, candidate)
	}

	// Deepest debt first — that is the order an admin works through.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Balance < candidates[j].Balance
	})

	return candidates, nil
}

// debtNotifyResult reports what happened to one selected member.
type debtNotifyResult struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	Tier   string `json:"tier,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// AdminQueueDebtEmailsHandler queues debt reminders for the selected members.
// POST /api/admin/email/debt-notify
//
// Every reminder goes into the outbox with a timer, so the admin can still
// review or cancel it from the settings page before it leaves. A zero timer
// means "due now": the entry is still recorded, it is just delivered on the
// spot by a background pass rather than inline, which keeps a large batch from
// running into the request timeout.
func (h *Handler) AdminQueueDebtEmailsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		UserIDs    []int64 `json:"user_ids"`
		DelayHours float64 `json:"delay_hours"`
		Force      bool    `json:"force"` // send despite a recent or pending reminder
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.UserIDs) == 0 {
		h.jsonError(w, "Nevybral jsi žádného člena", http.StatusBadRequest)
		return
	}
	// QueueEmail drops everything silently when email is switched off, which
	// would report a queued batch the admin would then never find in the outbox.
	if !h.config.EmailEnabled {
		h.jsonError(w, "E-maily jsou vypnuté (EMAIL_ENABLED=false), nic se nezařadilo", http.StatusConflict)
		return
	}
	if req.DelayHours < 0 {
		h.jsonError(w, "Timer nemůže být záporný", http.StatusBadRequest)
		return
	}
	delay := time.Duration(req.DelayHours * float64(time.Hour))
	if delay > maxManualDelay {
		h.jsonError(w, "Timer může být nejvýše 168 hodin (7 dní)", http.StatusBadRequest)
		return
	}

	queued, skipped, err := h.queueDebtReminders(ctx, req.UserIDs, delay, req.Force)
	if err != nil {
		h.jsonError(w, "Nepodařilo se načíst bilance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// A zero timer is due immediately; nudge the worker so the admin does not
	// wait for the next cron pass. Background context: the HTTP request is
	// already on its way out.
	if delay == 0 && len(queued) > 0 {
		go h.emailClient.ProcessPendingEmails(context.Background())
	}

	// Only needed for the audit line, so it is read after the request has
	// passed validation. Auth itself is enforced by the RequireAdmin middleware.
	adminEmail := ""
	if admin := h.auth.GetUser(r); admin != nil {
		adminEmail = admin.Email
	}
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "success",
		UserID:    sql.NullInt64{},
		Message: fmt.Sprintf("Manual debt reminders: %d queued, %d skipped (timer %.0fh, by %s)",
			len(queued), len(skipped), req.DelayHours, adminEmail),
		Metadata: sql.NullString{
			String: fmt.Sprintf(`{"queued":%d,"skipped":%d,"delay_hours":%.0f,"force":%t,"admin":%q}`,
				len(queued), len(skipped), req.DelayHours, req.Force, adminEmail),
			Valid: true,
		},
	})

	message := fmt.Sprintf("Zařazeno %d e-mailů do outboxu", len(queued))
	if delay == 0 && len(queued) > 0 {
		message = fmt.Sprintf("Zařazeno %d e-mailů, odesílají se", len(queued))
	}
	if len(skipped) > 0 {
		message += fmt.Sprintf(", %d přeskočeno", len(skipped))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": message,
		"queued":  queued,
		"skipped": skipped,
	})
}

// queueDebtReminders parks a reminder in the outbox for each selected member and
// reports what it did with every one of them. Members are re-derived from the
// live candidate list rather than trusted from the request, so a stale page
// cannot mail someone who has since paid up, nor let a caller pick the amount.
func (h *Handler) queueDebtReminders(ctx context.Context, userIDs []int64, delay time.Duration, force bool) (queued, skipped []debtNotifyResult, err error) {
	candidates, err := h.buildDebtCandidates(ctx)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]DebtCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.UserID] = c
	}

	// Park everything in the outbox rather than delivering inline, so one
	// unreachable SMTP server cannot stall the whole batch past the timeout.
	client := h.emailClient.Scheduled(delay)

	queued = make([]debtNotifyResult, 0, len(userIDs))
	skipped = make([]debtNotifyResult, 0)
	seen := make(map[int64]bool, len(userIDs))

	for _, userID := range userIDs {
		// A duplicate id in one request would otherwise mail the member twice.
		if seen[userID] {
			continue
		}
		seen[userID] = true

		candidate, ok := byID[userID]
		if !ok {
			skipped = append(skipped, debtNotifyResult{
				UserID: userID,
				Reason: "nemá dluh, nebo není aktivní člen",
			})
			continue
		}
		if candidate.Blocked && !force {
			skipped = append(skipped, debtNotifyResult{
				UserID: userID,
				Email:  candidate.Email,
				Reason: candidate.BlockReason,
			})
			continue
		}

		user, err := h.queries.GetUserByID(ctx, userID)
		if err != nil {
			skipped = append(skipped, debtNotifyResult{
				UserID: userID,
				Email:  candidate.Email,
				Reason: "uživatel nenalezen",
			})
			continue
		}

		err = client.SendDebtReminder(ctx, &user, candidate.Tier,
			float64(candidate.Balance), float64(candidate.MonthlyFee))
		if err != nil {
			log.Printf("[Handler] failed to queue debt reminder for %s: %v", candidate.Email, err)
			skipped = append(skipped, debtNotifyResult{
				UserID: userID,
				Email:  candidate.Email,
				Reason: err.Error(),
			})
			continue
		}

		queued = append(queued, debtNotifyResult{
			UserID: userID,
			Email:  candidate.Email,
			Tier:   candidate.Tier,
		})
	}

	return queued, skipped, nil
}
