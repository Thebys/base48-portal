package email

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/dues"
)

// The membership reminders, as template keys: one before any debt, two about
// debt. Which one a member gets is dues.Classify's call.
const (
	TierPrepaidEnding   = dues.PrepaidEnding
	TierNegativeBalance = dues.NegativeBalance
	TierDebtWarning     = dues.DebtWarning
)

// MembershipTiers lists them, mildest first.
var MembershipTiers = []string{TierPrepaidEnding, TierNegativeBalance, TierDebtWarning}

// IsMembershipTemplate reports whether an outbox template_name is one of the
// membership reminders.
func IsMembershipTemplate(file string) bool {
	for _, tier := range MembershipTiers {
		if file == TemplateFileForTier(tier) {
			return true
		}
	}
	return false
}

// TemplateFileForTier maps a reminder to its outbox template_name.
func TemplateFileForTier(tier string) string {
	if tier == "" {
		return ""
	}
	return tier + ".html"
}

// Settings keys of the membership reminders.
const (
	settingPrepaidEnabled     = "membership_prepaid_enabled"
	settingPrepaidMonthsAhead = "membership_prepaid_months_ahead"
	settingSuggestMonths      = "membership_suggest_months"
)

// MembershipSettings is what an admin sets for the membership reminders.
type MembershipSettings struct {
	// PrepaidEnabled sends the reminder before any debt. Off until an admin
	// switches it on, like any new kind of email to the whole membership.
	PrepaidEnabled bool
	// MonthsAhead: it goes to members who have not paid for this many months
	// after the current one (1: "paid up to the end of this month at most").
	MonthsAhead int
	// SuggestMonths is how many months ahead every reminder and the profile
	// suggest paying, on top of any debt.
	SuggestMonths int
}

// DefaultMembershipSettings is what applies before an admin saves anything.
func DefaultMembershipSettings() MembershipSettings {
	return MembershipSettings{PrepaidEnabled: false, MonthsAhead: 1, SuggestMonths: 3}
}

// Validate rejects values that would make no sense to members.
func (s MembershipSettings) Validate() error {
	if s.MonthsAhead < 1 || s.MonthsAhead > 3 {
		return errors.New("upozornění může jít 1 až 3 měsíce předem")
	}
	if s.SuggestMonths < 1 || s.SuggestMonths > 12 {
		return errors.New("doporučená platba může být na 1 až 12 měsíců")
	}
	return nil
}

// Policy is the part dues.Classify needs.
func (s MembershipSettings) Policy() dues.Policy {
	return dues.Policy{Prepaid: s.PrepaidEnabled, MonthsAhead: s.MonthsAhead}
}

// LoadMembershipSettings reads the settings, falling back to the default for
// any key that is missing or out of range.
func LoadMembershipSettings(ctx context.Context, q *db.Queries) MembershipSettings {
	s := DefaultMembershipSettings()
	get := func(key string) (string, bool) {
		row, err := q.GetSetting(ctx, key)
		return row.Value, err == nil
	}
	if v, ok := get(settingPrepaidEnabled); ok {
		s.PrepaidEnabled = v == "true"
	}
	if v, ok := get(settingPrepaidMonthsAhead); ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.MonthsAhead = n
		}
	}
	if v, ok := get(settingSuggestMonths); ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.SuggestMonths = n
		}
	}
	if s.Validate() != nil {
		d := DefaultMembershipSettings()
		d.PrepaidEnabled = s.PrepaidEnabled
		return d
	}
	return s
}

// SaveMembershipSettings validates and stores the settings.
func SaveMembershipSettings(ctx context.Context, q *db.Queries, s MembershipSettings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	for key, value := range map[string]string{
		settingPrepaidEnabled:     strconv.FormatBool(s.PrepaidEnabled),
		settingPrepaidMonthsAhead: strconv.Itoa(s.MonthsAhead),
		settingSuggestMonths:      strconv.Itoa(s.SuggestMonths),
	} {
		if _, err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: value}); err != nil {
			return fmt.Errorf("uložení %s selhalo: %w", key, err)
		}
	}
	return nil
}

// MembershipEmail is the data of the three membership reminders.
type MembershipEmail struct {
	Name      string
	PortalURL string
	Content   map[string]string
	Labels    map[string]string

	BalanceText     string // "−1 000 Kč"
	DebtText        string // "2 400 Kč"
	FeeText         string // "1 000 Kč"
	PaidThroughText string // "listopadu 2026": paid up to the end of it
	// ShortText is set when the member is a little short for ShortMonth, in
	// the reminder before any debt; that one then says so instead.
	ShortText  string
	ShortMonth string

	Payment Payment
}

// Payment is the box with what to pay where.
type Payment struct {
	Account, IBAN, BIC, VS string
	Message                string // what the QR carries; the VS is what counts
	Amount                 float64
	AmountText             string // the suggested payment, which the QR carries
	AmountNote             string // what it is made of and what it pays for
	MinimumText            string // at least this, when something is owed
	QR                     string // URL of the QR image
}

// PaymentMessage is the message for the payee the emails show. The QR code
// carries the same, as banks take it: CLENSKY PRISPEVEK BASE48 (see
// /api/qr). Banks show it on the statement; matching goes by the VS.
const PaymentMessage = "Členský příspěvek Base48"

// Month names: the genitive after "do konce", the nominative after "na".
var (
	monthsGenitive   = []string{"ledna", "února", "března", "dubna", "května", "června", "července", "srpna", "září", "října", "listopadu", "prosince"}
	monthsNominative = []string{"leden", "únor", "březen", "duben", "květen", "červen", "červenec", "srpen", "září", "říjen", "listopad", "prosinec"}
)

// MonthUntil is the month as it reads after "do konce" / "until the end of":
// "listopadu 2026", "November 2026".
func MonthUntil(m dues.Month, lang string) string {
	if lang == "en" {
		return fmt.Sprintf("%s %d", m.Month, m.Year)
	}
	return fmt.Sprintf("%s %d", monthsGenitive[m.Month-1], m.Year)
}

// MonthName is the bare month: "listopad", "November".
func MonthName(m dues.Month, lang string) string {
	if lang == "en" {
		return m.Month.String()
	}
	return monthsNominative[m.Month-1]
}

// MonthsCount is "3 měsíce", "5 měsíců", "1 month".
func MonthsCount(n int, lang string) string {
	if lang == "en" {
		if n == 1 {
			return "1 month"
		}
		return fmt.Sprintf("%d months", n)
	}
	switch {
	case n == 1:
		return "1 měsíc"
	case n >= 2 && n <= 4:
		return fmt.Sprintf("%d měsíce", n)
	default:
		return fmt.Sprintf("%d měsíců", n)
	}
}

// Crowns is a whole amount the Czech way, "3 000 Kč", in both languages: the
// emails always said Kč.
func Crowns(v float64) string {
	n := int64(v + 0.5)
	if v < 0 {
		n = int64(v - 0.5)
	}
	sign := ""
	if n < 0 {
		sign, n = "−", -n
	}
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(d)
	}
	return sign + b.String() + " Kč"
}

// placeholder is {name} in a text block.
var placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// fillPlaceholders replaces the placeholders in every block. Unknown ones are
// left as typed; the editor refuses them in templates that have placeholders.
func fillPlaceholders(content map[string]string, values map[string]string) {
	for block, text := range content {
		content[block] = placeholder.ReplaceAllStringFunc(text, func(m string) string {
			if v, ok := values[m[1:len(m)-1]]; ok {
				return v
			}
			return m
		})
	}
}

// membershipEmail builds a membership reminder for user in lang. It fails
// with bank.ErrUnknown until the FIO sync has recorded the account: a
// reminder without payment details is useless.
func (c *Client) membershipEmail(ctx context.Context, user *db.User, lang, tier string, st dues.Standing, s MembershipSettings) (MembershipEmail, error) {
	acc, ok := bank.Load(ctx, c.queries)
	if !ok {
		return MembershipEmail{}, bank.ErrUnknown
	}
	labels := EmailLabels(lang)

	short := tier == TierPrepaidEnding && st.Balance < 0
	amount := st.Suggest(s.SuggestMonths)
	covers := MonthUntil(st.Covers(amount), lang)
	months := MonthsCount(s.SuggestMonths, lang)

	e := MembershipEmail{
		Name:            displayName(user),
		PortalURL:       c.config.BaseURL,
		Labels:          labels,
		BalanceText:     Crowns(st.Balance),
		DebtText:        Crowns(st.Debt()),
		FeeText:         Crowns(st.Fee),
		PaidThroughText: MonthUntil(st.PaidThrough(), lang),
		Payment: Payment{
			Account:    acc.Domestic(),
			IBAN:       acc.PrettyIBAN(),
			BIC:        acc.BIC,
			VS:         user.PaymentsID.String,
			Message:    PaymentMessage,
			Amount:     amount,
			AmountText: Crowns(amount),
		},
	}
	switch {
	case short:
		e.ShortText = Crowns(st.Short())
		e.ShortMonth = MonthName(st.PaidThrough().Add(1), lang)
		e.Payment.AmountNote = fmt.Sprintf(labels["amount_note_short"], months, covers)
		e.Payment.MinimumText = e.ShortText
	case st.Debt() > 0:
		e.Payment.AmountNote = fmt.Sprintf(labels["amount_note_debt"], months, covers)
		e.Payment.MinimumText = e.DebtText
	default:
		e.Payment.AmountNote = fmt.Sprintf(labels["amount_note_ahead"], months, covers)
	}
	if e.Payment.VS != "" {
		e.Payment.QR = c.qrURL(e.Payment.VS, amount)
	}

	content := c.LoadContentBlocks(ctx, tier, lang)
	if short {
		// The variant for a member a little short replaces the blocks it has.
		for block, text := range content {
			if base, ok := strings.CutSuffix(block, "_short"); ok {
				content[base] = text
			}
		}
	}
	fillPlaceholders(content, map[string]string{
		"zaplaceno_do": e.PaidThroughText,
		"mesic":        e.ShortMonth,
		"chybi":        e.ShortText,
		"mesice":       months,
		"castka":       e.Payment.AmountText,
		"pokryje_do":   covers,
		"prispevek":    e.FeeText,
	})
	e.Content = content
	return e, nil
}

// qrURL is the address of the QR code an email shows: the payment portal's
// /api/qr, which encodes the account recorded by the FIO sync.
func (c *Client) qrURL(vs string, amount float64) string {
	return fmt.Sprintf("%s/api/qr?vs=%s&amount=%.0f", c.config.BaseURL, vs, amount)
}

// SendMembershipReminder queues the reminder tier for a member who stands at
// st. The monthly run and the admin's manual sender both go through here, and
// the check before sending renders through the same membershipEmail.
func (c *Client) SendMembershipReminder(ctx context.Context, user *db.User, tier string, st dues.Standing) error {
	if TemplateFileForTier(tier) == "" || !IsMembershipTemplate(TemplateFileForTier(tier)) {
		return fmt.Errorf("unknown membership reminder: %q", tier)
	}
	e, err := c.membershipEmail(ctx, user, userLang(user), tier, st, LoadMembershipSettings(ctx, c.queries))
	if err != nil {
		return err
	}
	return c.QueueEmail(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      e.Content["subject"],
		TemplateName: TemplateFileForTier(tier),
		Data:         e,
	})
}

// MembershipEmailFor builds a reminder for user in lang as it would go out,
// for the admin's previews and test emails.
func (c *Client) MembershipEmailFor(ctx context.Context, user *db.User, lang, tier string, st dues.Standing) (MembershipEmail, error) {
	return c.membershipEmail(ctx, user, lang, tier, st, LoadMembershipSettings(ctx, c.queries))
}

// membershipLook re-checks a membership reminder against the member's
// standing now. A debt reminder may turn into the other debt reminder, or be
// withdrawn once the debt is gone; it never becomes the friendly one, as the
// member has just acted. The friendly one is withdrawn when switched off or
// no longer warranted.
func (c *Client) membershipLook(ctx context.Context, entry *db.EmailOutbox) verdict {
	user, v := c.inState(ctx, entry, "accepted", "už není aktivní člen")
	if v.cancel != "" || v.err != nil {
		return v
	}
	level, err := c.queries.GetLevel(ctx, user.LevelID)
	if err != nil {
		return verdict{err: err}
	}
	now := time.Now()
	st, err := dues.Load(ctx, c.queries, &user, level.Amount, now)
	if err != nil {
		return verdict{err: err}
	}
	settings := LoadMembershipSettings(ctx, c.queries)
	tier := dues.Classify(st, dues.MonthOf(now), settings.Policy())

	queued := strings.TrimSuffix(entry.TemplateName, ".html")
	switch {
	case dues.IsDebt(queued) && !dues.IsDebt(tier):
		return verdict{cancel: fmt.Sprintf("bilance je teď %s, dluh mezitím uhrazen", Crowns(st.Balance))}
	case queued == TierPrepaidEnding && !settings.PrepaidEnabled:
		return verdict{cancel: "upozornění před koncem předplatného byla vypnuta"}
	case queued == TierPrepaidEnding && tier != TierPrepaidEnding:
		return verdict{cancel: fmt.Sprintf("už má zaplaceno do konce %s", MonthUntil(st.PaidThrough(), "cs"))}
	}

	e, err := c.membershipEmail(ctx, &user, userLang(&user), tier, st, settings)
	if err != nil {
		return verdict{err: err}
	}
	return verdict{file: TemplateFileForTier(tier), subject: e.Content["subject"], data: e}
}
