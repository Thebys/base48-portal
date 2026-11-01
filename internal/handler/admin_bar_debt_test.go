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

// The admin preview and test-send both go through buildEmailSample; the bar
// reminder must render there in both languages.
func TestBarDebtPreviewRenders(t *testing.T) {
	h, _ := testHandlerWithEmail(t)
	for _, lang := range []string{"cs", "en"} {
		sample, err := h.buildEmailSample(context.Background(), h.emailClient, email.TemplateBarDebt, "", lang, previewMember(lang))
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		html, err := h.emailClient.RenderPreview(email.SendParams{TemplateName: sample.File, Data: sample.Data})
		if err != nil {
			t.Fatalf("%s: render: %v", lang, err)
		}
		if !strings.Contains(html, "-350&nbsp;Kč") || strings.Contains(html, "%!") {
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
