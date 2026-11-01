package email

import (
	"context"
	"database/sql"
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

// verdict is the last look at a pending email: withdraw it, hold it (the
// lease brings it back), or send it, rendered afresh when file is set.
type verdict struct {
	cancel        string
	err           error
	file, subject string
	data          any
}

// cancelledPrefix starts the last_error of every entry withdrawn by the last look.
const cancelledPrefix = "Zrušeno před odesláním: "

// lastLook checks a pending entry against how things stand now. It only
// reads: refreshBeforeSend acts on the verdict, AsItWouldLeave shows it.
func (c *Client) lastLook(ctx context.Context, entry *db.EmailOutbox) verdict {
	if !entry.UserID.Valid {
		return verdict{}
	}
	var v verdict
	switch entry.TemplateName {
	case TemplateFileBarDebt:
		v = c.barDebtLook(ctx, entry)
	case TemplateFileForTier(TierPrepaidEnding), TemplateFileForTier(TierNegativeBalance), TemplateFileForTier(TierDebtWarning):
		v = c.membershipLook(ctx, entry)
	case "welcome.html":
		_, v = c.inState(ctx, entry, "accepted", "už není přijatý člen")
	case "registration.html":
		_, v = c.inState(ctx, entry, "awaiting", "registrace už byla vyřízena")
	case "membership_suspended.html":
		_, v = c.inState(ctx, entry, "suspended", "členství už není pozastavené")
	}
	return v
}

// inState loads the entry's recipient and withdraws the entry when they are
// gone or no longer in state: an admin who accepts a member and changes their
// mind within the delay must not have both emails go out.
func (c *Client) inState(ctx context.Context, entry *db.EmailOutbox, state, reason string) (db.User, verdict) {
	user, err := c.queries.GetUserByID(ctx, entry.UserID.Int64)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return user, verdict{cancel: "uživatel neexistuje"}
	case err != nil:
		return user, verdict{err: err}
	case user.State != state:
		return user, verdict{cancel: reason}
	}
	return user, verdict{}
}

// refreshBeforeSend gives a leased entry its last look and acts on it. ok says
// whether it may go out; when not, out says what became of it.
func (c *Client) refreshBeforeSend(ctx context.Context, entry *db.EmailOutbox) (out outcome, ok bool) {
	v := c.lastLook(ctx, entry)
	switch {
	case v.err != nil:
		log.Printf("[Email] cannot re-check outbox #%d, holding it: %v", entry.ID, v.err)
		return held, false
	case v.cancel != "":
		return c.cancelOutbox(ctx, entry, v.cancel), false
	case v.file != "":
		c.rerender(ctx, entry, v)
	}
	return delivered, true
}

// AsItWouldLeave is an outbox entry for the admin to look at: a pending one
// as the last look would send it now, or with why it would be withdrawn in
// LastError. Nothing is stored. One that cannot be checked or rendered now,
// and every entry no longer pending, is shown as it is.
func (c *Client) AsItWouldLeave(ctx context.Context, entry db.EmailOutbox) db.EmailOutbox {
	if entry.Status != "pending" {
		return entry
	}
	switch v := c.lastLook(ctx, &entry); {
	case v.cancel != "":
		entry.LastError = sql.NullString{String: "Kdyby odcházel teď, zrušil by se: " + v.cancel, Valid: true}
	case v.err == nil && v.file != "":
		c.renderAfresh(&entry, v)
	}
	return entry
}

// renderAfresh puts the verdict's fresh version of a reminder into entry,
// in memory. On failure entry is left as it was.
func (c *Client) renderAfresh(entry *db.EmailOutbox, v verdict) error {
	html, err := c.renderTemplate(SendParams{TemplateName: v.file, Data: v.data})
	if err != nil {
		return err
	}
	entry.Subject, entry.TemplateName = v.subject, v.file
	entry.RenderedHtml = sql.NullString{String: html, Valid: true}
	return nil
}

// rerender renders the entry afresh and stores the result for delivery. If
// rendering fails, the version from queue time is still a valid email and
// goes out unchanged.
func (c *Client) rerender(ctx context.Context, entry *db.EmailOutbox, v verdict) {
	if err := c.renderAfresh(entry, v); err != nil {
		log.Printf("[Email] re-render of outbox #%d failed, sending as queued: %v", entry.ID, err)
		return
	}
	if err := c.queries.UpdateEmailOutboxContent(ctx, db.UpdateEmailOutboxContentParams{
		Subject:      entry.Subject,
		TemplateName: entry.TemplateName,
		RenderedHtml: entry.RenderedHtml,
		ID:           entry.ID,
	}); err != nil {
		log.Printf("[Email] failed to store re-render of outbox #%d: %v", entry.ID, err)
	}
}

// cancelOutbox withdraws the leased entry, recording why in last_error so the
// admin sees the reason in the outbox. When that cannot be stored, the entry
// is held instead: it must not go out, and the lease brings it back.
func (c *Client) cancelOutbox(ctx context.Context, entry *db.EmailOutbox, reason string) outcome {
	lastError := sql.NullString{String: cancelledPrefix + reason, Valid: true}
	if err := c.queries.CancelLeasedEmailOutbox(ctx, db.CancelLeasedEmailOutboxParams{
		Reason: lastError,
		ID:     entry.ID,
	}); err != nil {
		log.Printf("[Email] failed to cancel outbox #%d: %v", entry.ID, err)
		return held
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
	return withdrawn
}
