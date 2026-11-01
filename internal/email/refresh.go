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

// Emails wait in the outbox (72h for membership reminders, 24h for the
// registration, welcome and suspension emails, a configurable delay for the
// bar) so an admin can review them. By the time they leave, the member may
// have paid, left, or moved on to another state. Just before delivery each
// one gets a last look: cancelled when no longer warranted, a reminder about
// money otherwise re-rendered with today's figures and current texts.
//
// Every delivery takes this look: the worker's, and an admin's send-now or
// retry, so a stale reminder cannot go out by hand either.

// look is the verdict of the last look.
type look int

const (
	lookSend      look = iota // deliver the entry as it now stands
	lookCancelled             // withdrawn here; the reason is in last_error
	lookHeld                  // could not check now; the lease brings it back
)

// cancelledPrefix starts the last_error of every entry withdrawn by the last look.
const cancelledPrefix = "Zrušeno před odesláním: "

// refreshBeforeSend checks a leased entry just before it is sent.
func (c *Client) refreshBeforeSend(ctx context.Context, entry *db.EmailOutbox) look {
	if !entry.UserID.Valid {
		return lookSend
	}
	switch entry.TemplateName {
	case TemplateFileBarDebt:
		return c.refreshBarDebt(ctx, entry)
	case TemplateFileForTier(TierPrepaidEnding), TemplateFileForTier(TierNegativeBalance), TemplateFileForTier(TierDebtWarning):
		return c.refreshMembershipReminder(ctx, entry)
	case "welcome.html":
		return c.stillInState(ctx, entry, "accepted", "už není přijatý člen")
	case "registration.html":
		return c.stillInState(ctx, entry, "awaiting", "registrace už byla vyřízena")
	case "membership_suspended.html":
		return c.stillInState(ctx, entry, "suspended", "členství už není pozastavené")
	default:
		return lookSend
	}
}

// stillInState cancels an entry announcing a state the recipient has left
// since it was queued: an admin who accepts a member and changes their mind
// within the delay must not have both emails go out.
func (c *Client) stillInState(ctx context.Context, entry *db.EmailOutbox, state, reason string) look {
	user, err := c.queries.GetUserByID(ctx, entry.UserID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return c.cancelOutbox(ctx, entry, "uživatel neexistuje")
	}
	if err != nil {
		log.Printf("[Email] cannot re-check outbox #%d: %v", entry.ID, err)
		return lookHeld
	}
	if user.State != state {
		return c.cancelOutbox(ctx, entry, reason)
	}
	return lookSend
}

// activeMemberFor loads the entry's recipient and cancels the entry when they
// are gone or no longer an accepted member. The verdict is lookSend only when
// the entry may go on.
func (c *Client) activeMemberFor(ctx context.Context, entry *db.EmailOutbox) (db.User, look) {
	user, err := c.queries.GetUserByID(ctx, entry.UserID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return user, c.cancelOutbox(ctx, entry, "uživatel neexistuje")
	}
	if err != nil {
		log.Printf("[Email] cannot re-check outbox #%d: %v", entry.ID, err)
		return user, lookHeld
	}
	if user.State != "accepted" {
		return user, c.cancelOutbox(ctx, entry, "už není aktivní člen")
	}
	return user, lookSend
}

// rerender renders the entry afresh and stores the result, updating the entry
// in place for delivery. If rendering fails, the version from queue time is
// still a valid email and goes out unchanged.
func (c *Client) rerender(ctx context.Context, entry *db.EmailOutbox, templateFile, subject string, data any) {
	rendered, err := c.renderTemplate(SendParams{TemplateName: templateFile, Data: data})
	if err != nil {
		log.Printf("[Email] re-render of outbox #%d failed, sending as queued: %v", entry.ID, err)
		return
	}
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

// cancelOutbox withdraws the leased entry, recording why in last_error so the
// admin sees the reason in the outbox. When that cannot be stored, the entry
// is held instead: it must not go out, and the lease brings it back.
func (c *Client) cancelOutbox(ctx context.Context, entry *db.EmailOutbox, reason string) look {
	lastError := sql.NullString{String: cancelledPrefix + reason, Valid: true}
	if err := c.queries.CancelLeasedEmailOutbox(ctx, db.CancelLeasedEmailOutboxParams{
		Reason: lastError,
		ID:     entry.ID,
	}); err != nil {
		log.Printf("[Email] failed to cancel outbox #%d: %v", entry.ID, err)
		return lookHeld
	}
	entry.Status = "cancelled"
	entry.LastError = lastError
	log.Printf("[Email] Cancelled outbox #%d (%s -> %s): %s", entry.ID, entry.TemplateName, entry.Recipient, reason)
	c.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "info",
		UserID:    entry.UserID,
		Message:   fmt.Sprintf("E-mail #%d pro %s zrušen před odesláním: %s", entry.ID, entry.Recipient, reason),
	})
	return lookCancelled
}
