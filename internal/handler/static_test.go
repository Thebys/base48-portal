package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticFilesListNoDirectories(t *testing.T) {
	static := http.StripPrefix("/static/", StaticFiles("../../web/static"))
	for path, want := range map[string]int{
		"/static/css/theme.css": http.StatusOK,
		"/static/js/html.js":    http.StatusOK,
		"/static/":              http.StatusNotFound,
		"/static/css/":          http.StatusNotFound,
		"/static/css":           http.StatusNotFound,
		"/static/missing.js":    http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		static.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
		if want == http.StatusOK && rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", path, rec.Header().Get("Cache-Control"))
		}
	}
}
