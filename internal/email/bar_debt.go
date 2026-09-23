package email

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/base48/member-portal/internal/db"
)

// Bar debt reminders: a member whose RevBank (kiosk) debt has gone above a
// threshold gets an email asking them to top it up. Unlike membership
// debt, a bar tab is settled in cash at the kiosk, so the email carries no bank
// details or QR code — paying by transfer would land on the membership balance.
//
// Everything about it is an admin setting (settings table), not an env var, so
// it can be switched off or retuned without a redeploy.

// TemplateBarDebt is the content-block key; TemplateFileBarDebt the outbox
// template_name the anti-spam lookup and the send-time check match on.
const (
	TemplateBarDebt     = "bar_debt"
	TemplateFileBarDebt = "bar_debt.html"
)

// Settings keys.
const (
	settingBarDebtEnabled   = "bar_debt_email_enabled"
	settingBarDebtThreshold = "bar_debt_email_threshold"
	settingBarDebtInterval  = "bar_debt_email_interval_days"
	settingBarDebtDelay     = "bar_debt_email_delay_hours"
)

// BarDebtSettings controls the bar debt reminder.
type BarDebtSettings struct {
	Enabled      bool  // off until an admin switches it on
	ThresholdCZK int64 // remind once the bar debt goes above this
	IntervalDays int   // at most one reminder per member per this many days
	DelayHours   int   // how long a reminder waits in the outbox before it leaves
}

// DefaultBarDebtSettings is what applies before an admin saves anything.
// Disabled by default: turning on a new kind of email to the whole membership
// should be a deliberate act.
func DefaultBarDebtSettings() BarDebtSettings {
	return BarDebtSettings{
		Enabled:      false,
		ThresholdCZK: 200,
		IntervalDays: 14,
		DelayHours:   24,
	}
}

// Validate rejects values that would either spam members or never fire.
func (s BarDebtSettings) Validate() error {
	if s.ThresholdCZK < 1 || s.ThresholdCZK > 100000 {
		return errors.New("hranice musí být mezi 1 a 100 000 Kč")
	}
	if s.IntervalDays < 1 || s.IntervalDays > 365 {
		return errors.New("interval musí být mezi 1 a 365 dny")
	}
	if s.DelayHours < 0 || s.DelayHours > 168 {
		return errors.New("zpoždění musí být mezi 0 a 168 hodinami")
	}
	return nil
}

// MaxBalanceCents is the highest bar balance that still warrants a reminder.
// The debt must go above the threshold, as the email says, so a debt of
// exactly the threshold does not count yet.
func (s BarDebtSettings) MaxBalanceCents() int64 {
	return -s.ThresholdCZK*100 - 1
}

// Warranted reports whether a bar balance is deep enough for a reminder.
func (s BarDebtSettings) Warranted(balanceCents int64) bool {
	return balanceCents <= s.MaxBalanceCents()
}

// LoadBarDebtSettings reads the settings, falling back to the default for any
// key that is missing or unparseable.
func LoadBarDebtSettings(ctx context.Context, q *db.Queries) BarDebtSettings {
	s := DefaultBarDebtSettings()

	get := func(key string) (string, bool) {
		row, err := q.GetSetting(ctx, key)
		if err != nil {
			return "", false
		}
		return row.Value, true
	}

	if v, ok := get(settingBarDebtEnabled); ok {
		s.Enabled = v == "true"
	}
	if v, ok := get(settingBarDebtThreshold); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			s.ThresholdCZK = n
		}
	}
	if v, ok := get(settingBarDebtInterval); ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.IntervalDays = n
		}
	}
	if v, ok := get(settingBarDebtDelay); ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.DelayHours = n
		}
	}
	return s
}

// SaveBarDebtSettings validates and stores the settings.
func SaveBarDebtSettings(ctx context.Context, q *db.Queries, s BarDebtSettings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	values := map[string]string{
		settingBarDebtEnabled:   strconv.FormatBool(s.Enabled),
		settingBarDebtThreshold: strconv.FormatInt(s.ThresholdCZK, 10),
		settingBarDebtInterval:  strconv.Itoa(s.IntervalDays),
		settingBarDebtDelay:     strconv.Itoa(s.DelayHours),
	}
	for key, value := range values {
		if _, err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: value}); err != nil {
			return fmt.Errorf("uložení %s selhalo: %w", key, err)
		}
	}
	return nil
}

// FormatCZKCents renders cents as Czech crowns: -35000 → "-350", -35050 → "-350,50".
func FormatCZKCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	if cents%100 == 0 {
		return fmt.Sprintf("%s%d", sign, cents/100)
	}
	return fmt.Sprintf("%s%d,%02d", sign, cents/100, cents%100)
}

// barDebtData builds the template data for a bar debt reminder.
func (c *Client) barDebtData(ctx context.Context, user *db.User, balanceCents, thresholdCZK int64) map[string]interface{} {
	lang := userLang(user)
	return map[string]interface{}{
		"Name":          displayName(user),
		"BalanceCents":  balanceCents,
		"BalanceText":   FormatCZKCents(balanceCents),
		"ThresholdText": strconv.FormatInt(thresholdCZK, 10),
		"PortalURL":     c.config.BaseURL,
		"Content":       c.LoadContentBlocks(ctx, TemplateBarDebt, lang),
		"Labels":        EmailLabels(lang),
	}
}

// SendBarDebt queues a bar debt reminder for one member.
func (c *Client) SendBarDebt(ctx context.Context, user *db.User, balanceCents, thresholdCZK int64) error {
	data := c.barDebtData(ctx, user, balanceCents, thresholdCZK)
	return c.QueueEmail(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      data["Content"].(map[string]string)["subject"],
		TemplateName: TemplateFileBarDebt,
		Data:         data,
	})
}

// BarDebtRunResult summarises one pass of QueueBarDebtReminders.
type BarDebtRunResult struct {
	Debtors int // accepted members above the threshold
	Queued  int
	Skipped int // reminded (or queued) within the interval
	Errors  int
}

// QueueBarDebtReminders queues a reminder for every accepted member whose bar
// debt is above the threshold and who has not had one within the
// interval. Safe to run repeatedly: a pending reminder counts as a recent one.
// Does nothing when the reminder is switched off.
func (c *Client) QueueBarDebtReminders(ctx context.Context, s BarDebtSettings) (BarDebtRunResult, error) {
	var res BarDebtRunResult
	if !s.Enabled {
		return res, nil
	}
	if err := s.Validate(); err != nil {
		return res, err
	}

	debtors, err := c.queries.ListAcceptedBarDebtors(ctx, s.MaxBalanceCents())
	if err != nil {
		return res, fmt.Errorf("list bar debtors: %w", err)
	}
	res.Debtors = len(debtors)

	client := c.Scheduled(time.Duration(s.DelayHours) * time.Hour)
	window := fmt.Sprintf("-%d days", s.IntervalDays)

	for _, d := range debtors {
		userID := sql.NullInt64{Int64: d.UserID, Valid: true}

		recent, err := c.queries.GetRecentEmailByUserAndTemplateWithin(ctx, db.GetRecentEmailByUserAndTemplateWithinParams{
			UserID:       userID,
			TemplateName: TemplateFileBarDebt,
			Window:       window,
		})
		if err == nil && recent.ID > 0 {
			res.Skipped++
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[Email] bar debt: anti-spam lookup failed for %s: %v", d.Email, err)
			res.Errors++
			continue
		}

		user, err := c.queries.GetUserByID(ctx, d.UserID)
		if err != nil {
			log.Printf("[Email] bar debt: user %d not found: %v", d.UserID, err)
			res.Errors++
			continue
		}

		if err := client.SendBarDebt(ctx, &user, d.BalanceCents, s.ThresholdCZK); err != nil {
			log.Printf("[Email] bar debt: failed to queue for %s: %v", d.Email, err)
			res.Errors++
			continue
		}
		res.Queued++
	}

	return res, nil
}

// refreshBarDebt re-checks a queued bar reminder against the live bar balance.
// A member topping up at the kiosk is the common case — then the reminder is
// cancelled. Otherwise it is re-rendered, so it quotes today's balance and the
// current template text rather than what applied when it was queued.
func (c *Client) refreshBarDebt(ctx context.Context, entry *db.EmailOutbox) bool {
	settings := LoadBarDebtSettings(ctx, c.queries)
	if !settings.Enabled {
		c.cancelOutbox(ctx, entry, "barové upomínky byly vypnuty")
		return false
	}
	user, ok := c.activeMemberFor(ctx, entry)
	if !ok {
		return false
	}

	account, err := c.queries.GetRevbankAccountByUserID(ctx, entry.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		c.cancelOutbox(ctx, entry, "barový účet nenalezen")
		return false
	}
	if err != nil {
		log.Printf("[Email] bar debt: cannot re-check outbox #%d: %v", entry.ID, err)
		return false
	}
	if !settings.Warranted(account.BalanceCents) {
		c.cancelOutbox(ctx, entry, fmt.Sprintf("zůstatek na baru je teď %s Kč, dluh už nepřesahuje hranici", FormatCZKCents(account.BalanceCents)))
		return false
	}

	data := c.barDebtData(ctx, &user, account.BalanceCents, settings.ThresholdCZK)
	c.rerender(ctx, entry, TemplateFileBarDebt, data)
	return true
}
