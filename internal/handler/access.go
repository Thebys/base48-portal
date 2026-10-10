package handler

import (
	"context"
	"log"
	"net/http"

	"github.com/base48/member-portal/internal/access"
	"github.com/base48/member-portal/internal/auth"
	"github.com/base48/member-portal/internal/db"
)

// accessFor resolves what the signed-in user may use. dbUser may be nil when
// there is no portal record to go by; then only the admin role counts.
// Grants that cannot be read are logged and treated as none.
func (h *Handler) accessFor(ctx context.Context, user *auth.User, dbUser *db.User) access.Subject {
	s := access.Subject{Admin: user.IsAdmin()}
	if dbUser == nil || dbUser.ID == 0 {
		return s
	}
	s.State = dbUser.State

	scopes, err := h.queries.ListAccessGrantScopes(ctx, dbUser.ID)
	if err != nil {
		log.Printf("[Access] Warning: cannot read grants of user %d: %v", dbUser.ID, err)
		return s
	}
	s.Grants = make(map[access.Scope]bool, len(scopes))
	for _, scope := range scopes {
		if access.Known(scope) {
			s.Grants[access.Scope(scope)] = true
		}
	}
	return s
}

// memberPage loads the signed-in user for a page and sends them to their
// profile when allowed says no. ok is false when a response has been written.
func (h *Handler) memberPage(w http.ResponseWriter, r *http.Request, allowed func(access.Subject) bool) (user *auth.User, dbUser *db.User, acc access.Subject, ok bool) {
	user = h.auth.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
		return nil, nil, acc, false
	}

	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return nil, nil, acc, false
	}

	acc = h.accessFor(r.Context(), user, dbUser)
	if !allowed(acc) {
		http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
		return nil, nil, acc, false
	}
	return user, dbUser, acc, true
}

// memberAPI is memberPage for JSON endpoints: it answers 403 instead of
// redirecting.
func (h *Handler) memberAPI(w http.ResponseWriter, r *http.Request, allowed func(access.Subject) bool) (dbUser *db.User, acc access.Subject, ok bool) {
	user := h.auth.GetUser(r)
	if user == nil {
		h.jsonError(w, "Nepřihlášen", http.StatusUnauthorized)
		return nil, acc, false
	}

	dbUser, err := h.getOrCreateUser(r, user)
	if err != nil {
		h.jsonError(w, "Database error", http.StatusInternalServerError)
		return nil, acc, false
	}

	acc = h.accessFor(r.Context(), user, dbUser)
	if !allowed(acc) {
		h.jsonError(w, "Nemáš přístup", http.StatusForbidden)
		return nil, acc, false
	}
	return dbUser, acc, true
}
