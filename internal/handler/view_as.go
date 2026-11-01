package handler

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/base48/member-portal/internal/auth"
)

// ViewAsHandler switches an admin to the portal as a member sees it, and back.
// The admin role is only hidden (auth.ViewAsMember), so no Keycloak call is
// needed either way.
// POST /view-as  as=member | as=admin
func (h *Handler) ViewAsHandler(w http.ResponseWriter, r *http.Request) {
	var asMember bool
	switch r.PostFormValue("as") {
	case "member":
		asMember = true
	case "admin":
	default:
		http.Error(w, "as must be member or admin", http.StatusBadRequest)
		return
	}

	if err := h.auth.ViewAsMember(w, r, asMember); err != nil {
		switch {
		case errors.Is(err, auth.ErrNotAdmin):
			http.Error(w, "Forbidden - admin access required", http.StatusForbidden)
		case errors.Is(err, auth.ErrNotSignedIn):
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		default:
			log.Printf("[Auth] Warning: cannot switch the member view: %v", err)
			http.Error(w, "Failed to save session", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, viewAsTarget(r.Referer(), asMember), http.StatusSeeOther)
}

// memberPages are the pages whose admin and member versions the nav swaps
// by role (layout.html), keyed by the admin path.
var memberPages = map[string]string{
	"/admin/finance":  "/finance",
	"/admin/projects": "/projects",
}

// viewAsTarget is where switching views lands: the page the switch was made
// on, or its counterpart in the other view. An admin-only page becomes the
// profile when switching to the member view, and so does a page that cannot
// be told. Only the path of the Referer is used, so the redirect stays on the
// portal whatever the header says.
func viewAsTarget(referer string, asMember bool) string {
	u, err := url.Parse(referer)
	if err != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, `\`) {
		return "/profile"
	}
	if asMember {
		if member, ok := memberPages[u.Path]; ok {
			return member
		}
		if u.Path == "/admin" || strings.HasPrefix(u.Path, "/admin/") {
			return "/profile"
		}
	} else {
		for admin, member := range memberPages {
			if member == u.Path {
				return admin
			}
		}
	}
	// Escaped: what came in encoded goes back out encoded.
	if u.RawQuery != "" {
		return u.EscapedPath() + "?" + u.RawQuery
	}
	return u.EscapedPath()
}
