package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	page := SecurityHeaders(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/qr" {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
	}))
	get := func(path string) http.Header {
		rec := httptest.NewRecorder()
		page.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Result().Header
	}

	h := get("/admin/users")
	for name, want := range map[string]string{
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "same-origin",
		"Strict-Transport-Security": "max-age=31536000",
		"Cache-Control":             "no-store",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, directive := range []string{"frame-ancestors 'none'", "object-src 'none'", "base-uri 'self'", "form-action 'self'", "connect-src 'self'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP %q lacks %q", csp, directive)
		}
	}

	if got := get("/static/js/nav.js").Get("Cache-Control"); got != "" {
		t.Errorf("static file Cache-Control = %q, want the file server's default", got)
	}
	if got := get("/api/qr").Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("QR Cache-Control = %q, the handler's own value should win", got)
	}

	rec := httptest.NewRecorder()
	SecurityHeaders(false)(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Result().Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS over plain http = %q, want none", got)
	}
}

// A page that loads a script from a host the CSP does not list breaks
// silently in production; this catches it here instead.
func TestCSPAllowsTheScriptsTemplatesLoad(t *testing.T) {
	scriptSrc := regexp.MustCompile(`script-src ([^;]*)`).FindStringSubmatch(contentSecurityPolicy)[1]
	allowed := strings.Fields(scriptSrc)
	external := regexp.MustCompile(`<script[^>]+src="(https?://[^"]+)"`)

	files, _ := filepath.Glob("../../web/templates/*.html")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range external.FindAllStringSubmatch(string(src), -1) {
			u, err := url.Parse(m[1])
			if err != nil {
				t.Fatal(err)
			}
			origin := u.Scheme + "://" + u.Host
			ok := false
			for _, a := range allowed {
				ok = ok || a == origin
			}
			if !ok {
				t.Errorf("%s loads %s, which script-src (%s) does not allow", filepath.Base(f), m[1], scriptSrc)
			}
		}
	}
}
