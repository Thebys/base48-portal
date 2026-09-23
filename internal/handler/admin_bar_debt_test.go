package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/base48/member-portal/internal/email"
)

// The admin preview and test-send both go through buildEmailTestData; the bar
// reminder must render there in both languages.
func TestBarDebtPreviewRenders(t *testing.T) {
	h, _ := testHandlerWithEmail(t)
	for _, lang := range []string{"cs", "en"} {
		file, data, err := h.buildEmailTestData(context.Background(), email.TemplateBarDebt, lang, "Jan Novák", "jan", "12345")
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		html, err := h.emailClient.RenderPreview(email.SendParams{TemplateName: file, Data: data})
		if err != nil {
			t.Fatalf("%s: render: %v", lang, err)
		}
		if !strings.Contains(html, "-350,50&nbsp;Kč") || strings.Contains(html, "%!") {
			t.Errorf("%s: preview does not show the sample balance cleanly", lang)
		}
	}
}

// Out-of-range settings are refused before anything is stored.
func TestSaveBarDebtSettingsRejectsInvalid(t *testing.T) {
	h, database := testHandlerWithEmail(t)

	body := []byte(`{"enabled":true,"threshold_czk":0,"interval_days":14,"delay_hours":24}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/bar-debt-settings", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.AdminSaveBarDebtSettingsHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'bar_debt_%'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d settings stored despite the rejection", n)
	}
}
