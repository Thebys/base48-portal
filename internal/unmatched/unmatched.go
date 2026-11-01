// Package unmatched sorts the incoming payments the sync could not assign to
// a member or project, the same way for the admin pages and for
// `portal-cron report`.
package unmatched

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/base48/member-portal/internal/db"
)

// MinAmount is the smallest incoming amount worth matching by hand. Below it
// are bank interest and test transfers. Outgoing payments are never matched
// to members, so they are below it too.
const MinAmount = 5.0

// Category says why a payment could not be assigned.
type Category string

const (
	EmptyVS      Category = "empty_vs"       // sent without a variable symbol
	UserNotFound Category = "user_not_found" // the VS is no member's payments_id
	SyncBug      Category = "sync_bug"       // the VS is a member's, yet it is unassigned
)

// Reason is the category in a sentence.
func (c Category) Reason() string {
	switch c {
	case EmptyVS:
		return "Empty variable symbol"
	case UserNotFound:
		return "No user with this payments_id exists"
	case SyncBug:
		return "User with this payments_id exists but payment not assigned (sync issue)"
	}
	return string(c)
}

// Payment is one unassigned incoming payment with its amount read and sorted.
type Payment struct {
	db.Payment
	Value    float64 // Amount as a number
	Category Category
}

// List returns the unassigned, not dismissed incoming payments of at least
// MinAmount, newest first. A VS is matched against members' payments_id, as
// the sync does; it is not a user id.
func List(ctx context.Context, q *db.Queries) ([]Payment, error) {
	rows, err := q.ListUnassignedPayments(ctx)
	if err != nil {
		return nil, err
	}
	var out []Payment
	for _, p := range rows {
		value, err := strconv.ParseFloat(p.Amount, 64)
		if err != nil || value < MinAmount {
			continue
		}
		item := Payment{Payment: p, Value: value, Category: EmptyVS}
		if p.Identification != "" {
			_, err := q.GetUserByPaymentsID(ctx, sql.NullString{String: p.Identification, Valid: true})
			switch {
			case err == sql.ErrNoRows:
				item.Category = UserNotFound
			case err != nil:
				return nil, err
			default:
				item.Category = SyncBug
			}
		}
		out = append(out, item)
	}
	return out, nil
}
