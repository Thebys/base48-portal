package handler

import (
	"net/http"
	"strings"
)

// contentSecurityPolicy is what pages may load and who may frame them.
//
// frame-ancestors stops other sites from framing the portal and tricking a
// signed-in member or admin into clicks (pentest F-05). The rest limits what
// injected markup could do: no plugins, no <base> hijack, forms only to us,
// fetch only to us, scripts only from us and the CDN the bar pages use.
// 'unsafe-inline' stays for scripts and styles because the templates use
// inline <script> blocks, onclick handlers and style attributes throughout.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// SecurityHeaders sets the headers every response gets (pentest F-05, F-06).
// hsts is for a portal served over https, where browsers should never try
// plain http again.
//
// Responses default to Cache-Control: no-store, since most of them are about
// a signed-in member and a shared browser must not keep them. Static files
// are left to the file server, and a handler that wants caching (the QR
// code) sets its own Cache-Control, which replaces this one.
func SecurityHeaders(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", contentSecurityPolicy)
			h.Set("X-Frame-Options", "DENY")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				h.Set("Cache-Control", "no-store")
			}
			next.ServeHTTP(w, r)
		})
	}
}
