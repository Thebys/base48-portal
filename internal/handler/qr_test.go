package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/base48/member-portal/internal/qrpay"
)

func TestQRPaymentRefusesWhatIsNoPayment(t *testing.T) {
	h := &Handler{qrpayService: qrpay.NewService("CZ6620100000002003436319", "FIOBCZPPXXX")}
	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.QRPaymentHandler(rec, httptest.NewRequest(http.MethodGet, "/api/qr?"+query, nil))
		return rec
	}

	for _, ok := range []string{"vs=110", "vs=110&amount=1000", "vs=1234567890&amount=0.5&msg=DAR", "vs=110&amount=9999999.99"} {
		if rec := get(ok); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("%s = %d %s, want a PNG", ok, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	for _, bad := range []string{
		// pentest F-12: NaN gave a code without an amount, Inf one with AM:+Inf
		"vs=110&amount=NaN", "vs=110&amount=Inf", "vs=110&amount=%2BInf", "vs=110&amount=-Inf",
		"vs=110&amount=-1", "vs=110&amount=10000000", "vs=110&amount=abc",
		"", "vs=", "vs=12345678901", "vs=110abc", "vs=ABC",
		"vs=110&msg=" + strings.Repeat("A", 201),
	} {
		if rec := get(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", bad, rec.Code)
		}
	}
}
