package email

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/base48/member-portal/internal/db"
)

// Reminders about money wait in the outbox (72h for membership debt, a
// configurable delay for the bar) so an admin can review them. By the time they
// leave, the member may have paid, left, or owe a different amount. Just
// before delivery each one gets a last look: cancelled when no longer
// warranted, otherwise re-rendered with today's figures and current texts.
//
// Only the scheduled worker (ProcessPendingEmails) does this. An admin pressing
// "send now" or "retry" is a deliberate act and goes out as it stands.

// refreshBeforeSend returns false when the entry must not be sent now — either
// it was cancelled here, or the check could not be made and the lease will
// bring it back on a later pass.
func (c *Client) refreshBeforeSend(ctx context.Context, entry *db.EmailOutbox) bool {
	if !entry.UserID.Valid {
		return true
	}
	switch entry.TemplateName {
	case TemplateFileBarDebt:
		return c.refreshBarDebt(ctx, entry)
	case TemplateFileForTier(TierNegativeBalance), TemplateFileForTier(TierDebtWarning):
		return c.refreshDebtReminder(ctx, entry)
	default:
		return true
	}
}

// refreshDebtReminder re-checks a membership debt reminder against the live
// balance. The tier may change on the way: a member who paid part of a debt
// warning gets the milder negative-balance email instead.
func (c *Client) refreshDebtReminder(ctx context.Context, entry *db.EmailOutbox) bool {
	user, ok := c.activeMemberFor(ctx, entry)
	if !ok {
		return false
	}

	level, err := c.queries.GetLevel(ctx, user.LevelID)
	if err != nil {
		log.Printf("[Email] debt: cannot re-check outbox #%d: %v", entry.ID, err)
		return false
	}
	balance, err := c.queries.GetUserBalance(ctx, db.GetUserBalanceParams{
		UserID:   sql.NullInt64{Int64: user.ID, Valid: true},
		UserID_2: user.ID,
	})
	if err != nil {
		log.Printf("[Email] debt: cannot re-check outbox #%d: %v", entry.ID, err)
		return false
	}

	monthlyFee := MonthlyFee(user.LevelActualAmount, level.Amount)
	tier := DebtTier(float64(balance), monthlyFee)
	if tier == "" {
		c.cancelOutbox(ctx, entry, fmt.Sprintf("bilance je teď %d Kč, dluh mezitím uhrazen", balance))
		return false
	}

	data := c.debtReminderData(ctx, &user, tier, float64(balance), monthlyFee)
	c.rerender(ctx, entry, TemplateFileForTier(tier), data)
	return true
}

// activeMemberFor loads the entry's recipient and cancels the entry when they
// are gone or no longer an accepted member. ok is false whenever the entry must
// not be sent now.
func (c *Client) activeMemberFor(ctx context.Context, entry *db.EmailOutbox) (user db.User, ok bool) {
	user, err := c.queries.GetUserByID(ctx, entry.UserID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		c.cancelOutbox(ctx, entry, "uživatel neexistuje")
		return user, false
	}
	if err != nil {
		log.Printf("[Email] cannot re-check outbox #%d: %v", entry.ID, err)
		return user, false
	}
	if user.State != "accepted" {
		c.cancelOutbox(ctx, entry, "už není aktivní člen")
		return user, false
	}
	return user, true
}

// rerender renders the entry afresh and stores the result, updating the entry
// in place for delivery. If rendering fails, the version from queue time is
// still a valid email and goes out unchanged.
func (c *Client) rerender(ctx context.Context, entry *db.EmailOutbox, templateFile string, data map[string]interface{}) {
	rendered, err := c.renderTemplate(SendParams{TemplateName: templateFile, Data: data})
	if err != nil {
		log.Printf("[Email] re-render of outbox #%d failed, sending as queued: %v", entry.ID, err)
		return
	}
	subject := data["Content"].(map[string]string)["subject"]
	dataJSON, _ := json.Marshal(data)
	if err := c.queries.UpdateEmailOutboxContent(ctx, db.UpdateEmailOutboxContentParams{
		Subject:      subject,
		TemplateName: templateFile,
		TemplateData: sql.NullString{String: string(dataJSON), Valid: true},
		RenderedHtml: sql.NullString{String: rendered, Valid: true},
		ID:           entry.ID,
	}); err != nil {
		log.Printf("[Email] failed to store re-render of outbox #%d: %v", entry.ID, err)
	}
	entry.Subject = subject
	entry.TemplateName = templateFile
	entry.RenderedHtml = sql.NullString{String: rendered, Valid: true}
}

// cancelOutbox withdraws a pending entry, recording why in last_error so the
// admin sees the reason in the outbox table.
func (c *Client) cancelOutbox(ctx context.Context, entry *db.EmailOutbox, reason string) {
	if _, err := c.queries.UpdateEmailOutboxStatus(ctx, db.UpdateEmailOutboxStatusParams{
		Status:    "cancelled",
		Attempts:  entry.Attempts,
		LastError: sql.NullString{String: "Zrušeno před odesláním: " + reason, Valid: true},
		ID:        entry.ID,
	}); err != nil {
		log.Printf("[Email] failed to cancel outbox #%d: %v", entry.ID, err)
		return
	}
	log.Printf("[Email] Cancelled outbox #%d (%s -> %s): %s", entry.ID, entry.TemplateName, entry.Recipient, reason)
	c.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "info",
		UserID:    entry.UserID,
		Message:   fmt.Sprintf("Upomínka #%d pro %s zrušena před odesláním: %s", entry.ID, entry.Recipient, reason),
	})
}
