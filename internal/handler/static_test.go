package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestFingerprintedAssetsAreKeptAYear(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "static", "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "static", "css", "theme.css")
	write := func(content string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{webRoot: root}

	write("body { color: black }", time.Now().Add(-time.Hour))
	first, err := h.assetURL("css/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "/static/css/theme.css?v=") {
		t.Fatalf("assetURL = %q", first)
	}
	if again, _ := h.assetURL("css/theme.css"); again != first {
		t.Errorf("same file, new address: %q, then %q", first, again)
	}
	write("body { color: var(--ink) }", time.Now())
	if changed, _ := h.assetURL("css/theme.css"); changed == first {
		t.Error("the address stayed when the file changed")
	}
	if _, err := h.assetURL("css/missing.css"); err == nil {
		t.Error("a missing file got an address")
	}

	static := http.StripPrefix("/static/", StaticFiles(filepath.Join(root, "static")))
	rec := httptest.NewRecorder()
	static.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, first, nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("fingerprinted Cache-Control = %q", got)
	}
}
