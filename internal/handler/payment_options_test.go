package handler

import (
	"testing"
	"time"

	"github.com/base48/member-portal/internal/dues"
)

func TestPaymentOptions(t *testing.T) {
	nov := dues.Month{Year: 2026, Month: time.November}
	owing := dues.Standing{Fee: 1000, Balance: -2400, Anchor: nov}

	options := paymentOptions(owing, "4848", 3, true)
	if len(options) != 4 {
		t.Fatalf("%d options, want 1, 3, 6 and 12 months", len(options))
	}
	var def paymentOption
	for _, o := range options {
		if o.Default {
			def = o
		}
	}
	if def.Months != 3 || def.Amount != 5400 || def.URL != "/api/qr?vs=4848&amount=5400" {
		t.Errorf("default %+v, want the debt and 3 months", def)
	}
	if want := "dluh a 3 měsíce dopředu, zaplaceno pak do konce února 2027"; def.Note != want {
		t.Errorf("note %q, want %q", def.Note, want)
	}

	// The admin's default joins the choices when it is not one of them.
	if got := paymentOptions(owing, "4848", 2, true); len(got) != 5 || !got[1].Default || got[1].Months != 2 {
		t.Errorf("suggest 2: %+v", got)
	}
	// Someone still awaiting approval is not told what it pays up to.
	if got := paymentOptions(dues.Standing{Fee: 1000, Anchor: nov}, "1", 3, false); got[1].Note != "3 měsíce dopředu" {
		t.Errorf("awaiting: note %q", got[1].Note)
	}
	// A free membership pays only a debt, if any.
	if got := paymentOptions(dues.Standing{Fee: 0, Balance: -500, Anchor: nov}, "1", 3, true); len(got) != 1 || got[0].Amount != 500 {
		t.Errorf("free with debt: %+v", got)
	}
	if got := paymentOptions(dues.Standing{Fee: 0, Anchor: nov}, "1", 3, true); got != nil {
		t.Errorf("free without debt: %+v", got)
	}
}
