// Package bank holds the account members pay into.
//
// The account is not configured anywhere. Every FIO statement describes the
// account its token reads, and the sync records that description here. The
// number in emails, QR codes and on the profile is therefore, by construction,
// the account payments are matched from. Changing the account means changing
// BANK_FIO_TOKEN; the next sync picks up the new one.
package bank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/db"
)

// settingKey is where the account lives in the settings table.
const settingKey = "bank_account"

// StaleAfter is how old the last confirmation may get before the admin page
// warns that the FIO sync has stopped confirming the account.
const StaleAfter = 24 * time.Hour

// ErrUnknown is returned where a payment account is required but no FIO sync
// has recorded one yet.
var ErrUnknown = errors.New("platební účet zatím není známý: zaznamená ho první úspěšný FIO sync")

// Account is the bank account members pay into, as FIO last reported it.
type Account struct {
	IBAN      string    `json:"iban"`       // CZ6620100000002003436319
	BIC       string    `json:"bic"`        // FIOBCZPPXXX, may be empty
	Number    string    `json:"number"`     // 2003436319, or 19-2000145399 with a prefix
	BankCode  string    `json:"bank_code"`  // 2010
	CheckedAt time.Time `json:"checked_at"` // last sync that reported this account
}

// Domestic returns the Czech form of the number, e.g. "2003436319/2010".
func (a Account) Domestic() string {
	return a.Number + "/" + a.BankCode
}

// PrettyIBAN returns the IBAN in groups of four, the way banks print it.
func (a Account) PrettyIBAN() string {
	var b strings.Builder
	for i, r := range a.IBAN {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Stale reports whether the account has gone unconfirmed for too long.
func (a Account) Stale(now time.Time) bool {
	return now.Sub(a.CheckedAt) > StaleAfter
}

func (a Account) normalized() Account {
	a.IBAN = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(a.IBAN), " ", ""))
	a.BIC = strings.ToUpper(strings.TrimSpace(a.BIC))
	a.Number = strings.TrimSpace(a.Number)
	a.BankCode = strings.TrimSpace(a.BankCode)
	return a
}

// Validate checks that the account is a well-formed Czech IBAN and that the
// domestic number and bank code are the ones the IBAN encodes. A statement
// that disagrees with itself is not something to send members to.
func (a Account) Validate() error {
	if len(a.IBAN) != 24 || !strings.HasPrefix(a.IBAN, "CZ") {
		return fmt.Errorf("IBAN %q není český IBAN (CZ a 22 číslic)", a.IBAN)
	}
	if !allDigits(a.IBAN[2:]) {
		return fmt.Errorf("IBAN %q obsahuje jiné znaky než číslice", a.IBAN)
	}
	if !ibanChecksumOK(a.IBAN) {
		return fmt.Errorf("IBAN %q má chybný kontrolní součet", a.IBAN)
	}

	if a.BankCode != a.IBAN[4:8] {
		return fmt.Errorf("kód banky %q nesedí na IBAN %s", a.BankCode, a.IBAN)
	}
	prefix, number := "0", a.Number
	if p, n, ok := strings.Cut(a.Number, "-"); ok {
		prefix, number = p, n
	}
	if !allDigits(prefix) || !allDigits(number) || number == "" ||
		trimZeros(prefix) != trimZeros(a.IBAN[8:14]) || trimZeros(number) != trimZeros(a.IBAN[14:24]) {
		return fmt.Errorf("číslo účtu %q nesedí na IBAN %s", a.Number, a.IBAN)
	}

	if a.BIC != "" && (len(a.BIC) != 8 && len(a.BIC) != 11 || !allAlnum(a.BIC)) {
		return fmt.Errorf("BIC %q není platný", a.BIC)
	}
	return nil
}

// Load returns the recorded account. ok is false until the first successful
// FIO sync, or if the stored value cannot be used.
func Load(ctx context.Context, q *db.Queries) (acc Account, ok bool) {
	row, err := q.GetSetting(ctx, settingKey)
	if err != nil {
		return Account{}, false
	}
	if err := json.Unmarshal([]byte(row.Value), &acc); err != nil || acc.Validate() != nil {
		return Account{}, false
	}
	return acc, true
}

// Record stores the account a FIO statement described, confirmed at now.
// It is called on every sync. When the account differs from the stored one
// (a new token), the change goes to the system log so it leaves a trace.
// An invalid account is rejected and the stored one is kept.
func Record(ctx context.Context, q *db.Queries, acc Account, now time.Time) (Account, error) {
	acc = acc.normalized()
	if err := acc.Validate(); err != nil {
		return Account{}, err
	}
	acc.CheckedAt = now.UTC()

	prev, known := Load(ctx, q)
	raw, err := json.Marshal(acc)
	if err != nil {
		return Account{}, err
	}
	if _, err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settingKey, Value: string(raw)}); err != nil {
		return Account{}, fmt.Errorf("uložení platebního účtu selhalo: %w", err)
	}

	switch {
	case !known:
		q.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "fio_sync",
			Level:     "info",
			Message:   fmt.Sprintf("Platební účet zaznamenán z FIO: %s (%s)", acc.Domestic(), acc.IBAN),
		})
	case prev.IBAN != acc.IBAN:
		q.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "fio_sync",
			Level:     "warning",
			Message: fmt.Sprintf("Platební účet se změnil: %s → %s. Nový účet se od teď uvádí v QR kódech, upomínkách i na profilu.",
				prev.Domestic(), acc.Domestic()),
		})
	}
	return acc, nil
}

// ibanChecksumOK runs the ISO 13616 mod-97 check: move the country code and
// check digits to the end, turn letters into numbers (A=10 … Z=35), and the
// result must leave remainder 1.
func ibanChecksumOK(iban string) bool {
	rearranged := iban[4:] + iban[:4]
	var digits strings.Builder
	for _, r := range rearranged {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			fmt.Fprintf(&digits, "%d", r-'A'+10)
		default:
			return false
		}
	}
	n, ok := new(big.Int).SetString(digits.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func allAlnum(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func trimZeros(s string) string {
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	return s
}
