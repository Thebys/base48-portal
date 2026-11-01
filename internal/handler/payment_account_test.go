package handler

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
	"github.com/base48/member-portal/internal/qrpay"
)

// recordPaymentAccount does what the FIO sync does: it records the account the
// token reads, confirmed at the given time.
func recordPaymentAccount(t *testing.T, q *db.Queries, at time.Time) {
	t.Helper()
	if _, err := bank.Record(context.Background(), q, bank.Account{
		IBAN: "CZ6620100000002003436319", BIC: "FIOBCZPPXXX", Number: "2003436319", BankCode: "2010",
	}, at); err != nil {
		t.Fatalf("record payment account: %v", err)
	}
}

func TestQRWaitsForThePaymentAccount(t *testing.T) {
	q, _ := testQueries(t)
	h := &Handler{queries: q, config: &config.Config{}}
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.QRPaymentHandler(rec, httptest.NewRequest(http.MethodGet, "/api/qr?vs=709&amount=1400", nil))
		return rec
	}

	if rec := get(); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before any FIO sync: status %d, want 503", rec.Code)
	}

	// The sync records the account; the next request uses it, no restart.
	recordPaymentAccount(t, q, time.Now())
	rec := get()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("after the sync: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	svc, _, ok := h.paymentQR(context.Background())
	if !ok {
		t.Fatal("paymentQR should know the account")
	}
	spayd := svc.GenerateSPAYDString(qrpay.GenerateParams{VariableSymbol: "709", Amount: 1400})
	if !strings.Contains(spayd, "ACC:CZ6620100000002003436319+FIOBCZPPXXX*") {
		t.Errorf("QR encodes %q, want the recorded account", spayd)
	}
}

// renderSettings renders the settings page the way AdminSettingsHandler does,
// with the payment account part built by the same helper.
func renderSettings(t *testing.T, h *Handler, now time.Time) string {
	t.Helper()
	data := map[string]interface{}{
		"Title": "Nastavení",
		// Not an *auth.User, so render does not ask Keycloak for applications.
		"User":         map[string]interface{}{"Email": "admin@base48.cz", "PreferredName": "admin", "IsAdmin": true},
		"DBUser":       db.User{State: "accepted"},
		"OutboxCounts": map[string]int64{"pending": 0, "sent": 0, "failed": 0, "cancelled": 0},
		"BarDebt":      email.DefaultBarDebtSettings(),
	}
	h.addPaymentAccountView(context.Background(), data, now)
	rec := httptest.NewRecorder()
	h.render(rec, "admin_settings.html", data)
	if rec.Code != http.StatusOK {
		t.Fatalf("render: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestSettingsShowThePaymentAccount(t *testing.T) {
	now := time.Now()

	t.Run("before the first sync, without a token", func(t *testing.T) {
		q, _ := testQueries(t)
		h := &Handler{queries: q, config: &config.Config{}, webRoot: "../../web"}
		page := renderSettings(t, h, now)
		for _, want := range []string{"Platební účet", "Neznámý", "BANK_FIO_TOKEN není nastavený"} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	})

	t.Run("confirmed by a recent sync", func(t *testing.T) {
		q, _ := testQueries(t)
		recordPaymentAccount(t, q, now.Add(-2*time.Minute))
		h := &Handler{queries: q, config: &config.Config{BankFIOToken: "token"}, webRoot: "../../web"}
		page := renderSettings(t, h, now)
		for _, want := range []string{"Ověřený", "2003436319/2010", "CZ66 2010 0000 0020 0343 6319", "FIOBCZPPXXX"} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
		for _, unwanted := range []string{"Neznámý", "Neověřený", "2800691518"} {
			if strings.Contains(page, unwanted) {
				t.Errorf("page shows %q", unwanted)
			}
		}
	})

	t.Run("sync stopped confirming it", func(t *testing.T) {
		q, _ := testQueries(t)
		recordPaymentAccount(t, q, now.Add(-2*bank.StaleAfter))
		h := &Handler{queries: q, config: &config.Config{BankFIOToken: "token"}, webRoot: "../../web"}
		page := renderSettings(t, h, now)
		for _, want := range []string{"Neověřený", "starší než den", "2003436319/2010"} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q", want)
			}
		}
	})
}

// The profile pages are only parsed here; the account block is a plain
// {{with}} over a value the profile builder sets when the account is known.
func TestProfileTemplatesParse(t *testing.T) {
	for _, name := range []string{"profile.html", "admin_user_profile.html"} {
		_, err := template.New("").Funcs((&Handler{}).templateFuncs()).ParseFiles(filepath.Join("../../web/templates", "layout.html"), filepath.Join("../../web/templates", name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
