package handler

import (
	"net/http"
	"net/url"
)

// SameOriginWrites refuses a POST, PUT, PATCH or DELETE that a browser sent
// from another site (pentest F-11). The forms and admin APIs carry no CSRF
// token; until now only the cookie's SameSite=Lax kept other sites out.
//
// Browsers say where a request comes from: every current one sends
// Sec-Fetch-Site, older ones at least Origin on a POST. A request with
// neither is not from a browser (the RevBank sync, curl) and passes; it has
// no member's cookie to abuse.
func SameOriginWrites(baseURL string) func(http.Handler) http.Handler {
	origin := ""
	if u, err := url.Parse(baseURL); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				if !fromOurPages(r, origin) {
					http.Error(w, "Forbidden - cross-site request", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func fromOurPages(r *http.Request, origin string) bool {
	// "same-site" is refused too: for SameSite every *.base48.cz host is the
	// same site, so any service there could post here with the cookie.
	// "none" is a request the user started (a bookmark, the address bar).
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	if o := r.Header.Get("Origin"); o != "" {
		return o == origin
	}
	return true
}
