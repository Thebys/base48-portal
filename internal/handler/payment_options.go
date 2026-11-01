package handler

import (
	"fmt"
	"slices"

	"github.com/base48/member-portal/internal/dues"
	"github.com/base48/member-portal/internal/email"
)

// paymentOption is one amount the profile offers to pay by QR: any debt and
// a number of months ahead.
type paymentOption struct {
	Months  int
	Label   string  // "3 měsíce"
	Amount  float64 // whole crowns
	Note    string  // what it is made of and what it pays for
	URL     string  // the QR code for it
	Default bool
}

// offeredMonths are the choices on the profile, besides the admin's default.
var offeredMonths = []int{1, 3, 6, 12}

// paymentOptions is what the profile offers a member who stands at st. The
// default is the one the reminders suggest. paidUp says the member is
// charged monthly, so a note may say what each amount pays up to; for one
// still awaiting approval it would be a guess.
func paymentOptions(st dues.Standing, vs string, suggest int, paidUp bool) []paymentOption {
	if st.Fee <= 0 {
		if st.Debt() <= 0 {
			return nil
		}
		return []paymentOption{{Amount: st.Debt(), Note: "dluh", URL: qrLink(vs, st.Debt()), Default: true}}
	}

	months := slices.Clone(offeredMonths)
	if !slices.Contains(months, suggest) {
		months = append(months, suggest)
		slices.Sort(months)
	}
	options := make([]paymentOption, 0, len(months))
	for _, n := range months {
		amount := st.Suggest(n)
		label := email.MonthsCount(n, "cs")
		note := label + " dopředu"
		if st.Debt() > 0 {
			note = "dluh a " + note
		}
		if paidUp {
			note += ", zaplaceno pak do konce " + email.MonthUntil(st.Covers(amount), "cs")
		}
		options = append(options, paymentOption{
			Months: n, Label: label, Amount: amount, Note: note,
			URL: qrLink(vs, amount), Default: n == suggest,
		})
	}
	return options
}

func qrLink(vs string, amount float64) string {
	return fmt.Sprintf("/api/qr?vs=%s&amount=%.0f", vs, amount)
}
