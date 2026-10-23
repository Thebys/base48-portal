package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOriginWrites(t *testing.T) {
	reached := false
	h := SameOriginWrites("https://members.base48.cz")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	tests := []struct {
		name    string
		method  string
		headers map[string]string
		allowed bool
	}{
		{"own form", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://members.base48.cz"}, true},
		{"own fetch, delete", http.MethodDelete, map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"another site", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		// SameSite=Lax lets these through with the cookie; this must not.
		{"a sibling subdomain", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://wiki.base48.cz"}, false},
		{"old browser, other origin", http.MethodPost, map[string]string{"Origin": "https://evil.example"}, false},
		{"old browser, our origin", http.MethodPost, map[string]string{"Origin": "https://members.base48.cz"}, true},
		{"old browser, http origin", http.MethodPost, map[string]string{"Origin": "http://members.base48.cz"}, false},
		{"opaque origin", http.MethodPost, map[string]string{"Origin": "null"}, false},
		{"not a browser (RevBank sync)", http.MethodPost, nil, true},
		{"cross-site GET is a link", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
	}
	for _, tt := range tests {
		reached = false
		req := httptest.NewRequest(tt.method, "/api/admin/users/1/state", nil)
		for k, v := range tt.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if reached != tt.allowed {
			t.Errorf("%s: reached handler = %v, want %v (status %d)", tt.name, reached, tt.allowed, rec.Code)
		}
		if !tt.allowed && rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", tt.name, rec.Code)
		}
	}
}
