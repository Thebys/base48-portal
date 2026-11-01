package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net"
	"net/smtp"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
)

// Client handles email sending with templates, outbox, and logging
type Client struct {
	config  *config.Config
	queries *db.Queries

	// DefaultDelay is how long a queued email waits in the outbox before it is
	// due, so an admin can review or cancel it. Zero means due at once.
	DefaultDelay time.Duration

	// dueAt, when set (see At), is the time queued emails are due instead.
	dueAt time.Time
}

// SendParams contains parameters for sending a templated email
type SendParams struct {
	UserID       sql.NullInt64
	Recipient    string
	Subject      string
	TemplateName string
	Data         interface{}
}

// New creates a new email client. Payment details come from the account the
// FIO sync recorded (package bank), read whenever an email is rendered, so
// every process renders the same details and none can be built without them.
func New(cfg *config.Config, queries *db.Queries) *Client {
	return &Client{
		config:  cfg,
		queries: queries,
	}
}

// Scheduled returns a shallow copy of the client whose emails are due after the
// given delay. A zero delay means "due immediately": the entry still lands in
// the outbox and is picked up on the next worker pass. Used where an admin
// picks the timer, and for bar reminders.
func (c *Client) Scheduled(delay time.Duration) *Client {
	clone := *c
	clone.DefaultDelay = delay
	clone.dueAt = time.Time{}
	return &clone
}

// At returns a shallow copy of the client whose emails are due at t.
func (c *Client) At(t time.Time) *Client {
	clone := *c
	clone.dueAt = t
	return &clone
}

// LocalHourAfter is the first time at or after t when the local clock shows
// the given full hour. Reminders leave at a daytime hour rather than at
// whatever time of night the job that queued them ran.
func LocalHourAfter(t time.Time, hour int) time.Time {
	t = t.Local()
	at := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, time.Local)
	if at.Before(t) {
		at = time.Date(t.Year(), t.Month(), t.Day()+1, hour, 0, 0, 0, time.Local)
	}
	return at
}

// Czech typography: non-breaking space after prepositions/conjunctions and before "Kč".
// Covers single-letter (v, s, k, z, u, o, i, a),
// two-letter (se, si, na, ze, do, po, ve, ke, za, od, ku, by, co, že),
// three-letter (bez, nad, pod, pro, při, ale, ani, než).
// Prefix includes \u00a0 so consecutive prepositions ("se na") are handled via loop.
var (
	reCzechPrep = regexp.MustCompile(`(?i)(^|[ \n\x{00a0}])(se|si|na|ze|do|po|ve|ke|za|od|ku|by|co|že|bez|nad|pod|pro|při|ale|ani|než|[vszkuoia]) `)
	reCurrency  = regexp.MustCompile(`(\d) (Kč)`)
)

// fixCzechTypography replaces regular spaces with non-breaking spaces (U+00A0)
// after Czech prepositions/conjunctions and before "Kč". Uses Unicode NBSP
// so html/template doesn't escape it; renderTemplate converts to &nbsp; afterwards.
func fixCzechTypography(s string) string {
	const nbsp = "\u00a0"
	// Loop: each pass may reveal new matches (e.g. "se na" → "se\u00a0na " → next pass fixes "na ")
	for {
		next := reCzechPrep.ReplaceAllString(s, "${1}${2}"+nbsp)
		if next == s {
			break
		}
		s = next
	}
	s = reCurrency.ReplaceAllString(s, "${1}"+nbsp+"${2}")
	s = strings.ReplaceAll(s, " Kč", nbsp+"Kč")
	return s
}

// Default content blocks per template — Czech (editable via admin UI)
var defaultContentBlocks = map[string]map[string]string{
	"registration": {
		"subject":         "Tvůj účet v Base48 je připraven",
		"heading":         "Tvůj účet je připraven",
		"preheader":       "Tvůj účet na portálu Base48 je připraven.",
		"intro_text":      "Tvůj účet na členském portálu Base48 je vytvořen.",
		"next_steps_text": "Co bude dál: tvé členství bude schváleno na nejbližším setkání komunity. Jakmile budeš přijat/a, pošleme ti uvítací e-mail s dalšími informacemi. Obvykle to trvá týden nebo dva.",
		"wiki_text":       "Mezitím se můžeš podívat na naši wiki:\n→ https://wiki.base48.cz",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Máš otázky? Stačí odpovědět na tento e-mail.",
	},
	"welcome": {
		"subject":         "Vítej v Base48, jsi členem!",
		"heading":         "Vítej v Base48, jsi členem! 🎉",
		"preheader":       "Jsi členem Base48! Vítej na palubě.",
		"intro_text":      "Tvé členství v Base48 je aktivní. Vítej na palubě.",
		"resources_text":  "1. PŘIPOJ SE KE KONVERZACI\n   Matrix: https://matrix.to/#/#base48:matrix.org\n\n2. PŘEČTI SI PRAVIDLA\n   → https://wiki.base48.cz/wiki/Rules\n\n3. PROZKOUMEJ WIKI\n   Návody k nástrojům, dokumentace projektů a vše ostatní:\n   → https://wiki.base48.cz",
		"membership_text": "Tvé členství je aktivní. Přihlas se do portálu, zkontroluj svůj variabilní symbol a nastav si výši příspěvku.",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Máš otázky? Stačí odpovědět na tento e-mail.",
	},
	"prepaid_ending": {
		"subject":          "Členství v Base48 máš zaplacené do konce {zaplaceno_do}",
		"subject_short":    "Na {mesic} ti v Base48 chybí {chybi}",
		"preheader":        "Díky za podporu! Pokud můžeš, pošli příspěvek aspoň na {mesice} dopředu.",
		"preheader_short":  "Doplať prosím {chybi} na {mesic}, ideálně rovnou na {mesice} dopředu.",
		"heading":          "Díky za podporu Base48",
		"heading_short":    "Na {mesic} ti chybí {chybi}",
		"intro_text":       "díky, že platíš členské příspěvky. Takhle na tom teď jsi:",
		"intro_text_short": "na členský příspěvek za {mesic} ti ještě něco chybí. Nejde o upomínku, jen ať o tom víš dřív, než se z toho stane dluh:",
		"prepay_text":      "Další příspěvek se předepíše začátkem příštího měsíce. Pokud můžeš, pošli ho rovnou aspoň na {mesice} dopředu: méně plateb pro tebe, méně párování pro nás a spolek ví, s čím může počítat. Díky!",
		"sign_off":         "Base48 hackerspace",
		"footer":           "Máš otázky, nebo ti něco nesedí? Stačí odpovědět na tento e-mail.",
	},
	"negative_balance": {
		"subject":     "Záporná bilance členského příspěvku",
		"heading":     "Záporná bilance členského příspěvku",
		"preheader":   "Tvá bilance členského příspěvku je záporná.",
		"intro_text":  "Tvá aktuální bilance členského příspěvku je záporná:",
		"action_text": "To znamená, že dlužíš Base48 za členské příspěvky. Prosíme tě o úhradu co nejdříve.",
		"sign_off":    "Base48 hackerspace",
		"footer":      "Pokud máš dotazy ohledně své bilance nebo máš problémy s platbou, kontaktuj nás prosím.",
	},
	"debt_warning": {
		"subject":           "⚠️ Upozornění na dluh za členství",
		"heading":           "⚠️ Upozornění na dluh za členství",
		"preheader":         "Tvůj dluh přesáhl dvojnásobek měsíčního příspěvku.",
		"warning_text":      "Tvůj dluh za členské příspěvky přesáhl dvojnásobek měsíčního poplatku.",
		"consequences_text": "Pokud dluh nebude uhrazen v brzké době, může dojít k pozastavení členství a omezení přístupu do prostoru.",
		"steps_text":        "1. Uhraď dluh co nejdříve pomocí níže uvedených platebních údajů\n2. Pokud máš finanční problémy, kontaktuj nás - můžeme se domluvit na splátkách nebo snížení příspěvku\n3. Zkontroluj si v portálu, zda všechny tvé platby byly správně přiřazeny",
		"sign_off":          "Base48 hackerspace",
		"footer":            "Potřebuješ pomoc? Pokud máš dotazy nebo potřebuješ domluvit individuální řešení, neváhej nás kontaktovat. Jsme tu, abychom ti pomohli.",
	},
	"membership_suspended": {
		"subject":           "Pozastavení členství v Base48",
		"heading":           "Pozastavení členství v Base48",
		"preheader":         "Tvé členství v Base48 bylo pozastaveno.",
		"intro_text":        "Tvé členství v Base48 bylo pozastaveno.",
		"consequences_text": "Přístup do prostoru Base48 je dočasně omezen. Členské benefity jsou pozastaveny.",
		"recovery_text":     "Zkontroluj si svou bilanci v portálu. Pokud je důvodem neuhrazený dluh, uhraď prosím částku co nejdříve.",
		"sign_off":          "Base48 hackerspace",
		"footer":            "Pokud si myslíš, že došlo k omylu, neváhej nás kontaktovat.",
	},
	"bar_debt": {
		"subject":     "Dluh na baru v Base48",
		"heading":     "Máš dluh na baru",
		"preheader":   "Tvůj barový účet v Base48 je v minusu.",
		"intro_text":  "Tvůj účet na barovém kiosku je v minusu:",
		"action_text": "Dluh si prosím vyrovnej při příští návštěvě: na kiosku naskenuj částku dobití, pak pípni svou kartičku (nebo napiš přezdívku) a vlož hotovost do pokladny.",
		"sign_off":    "Base48 hackerspace",
		"footer":      "Pokud ti zůstatek nesedí, dej nám vědět. Stačí odpovědět na tento e-mail.",
	},
}

// Default content blocks — English
var defaultContentBlocksEN = map[string]map[string]string{
	"registration": {
		"subject":         "Your Base48 account is ready",
		"heading":         "Your account is ready",
		"preheader":       "Your Base48 portal account is ready.",
		"intro_text":      "Your account on the Base48 member portal is set up.",
		"next_steps_text": "What happens next: your membership will be reviewed at our next community meeting. We'll email you as soon as you're approved, usually within a week or two.",
		"wiki_text":       "While you wait, check out our wiki:\n→ https://wiki.base48.cz",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Questions? Just reply to this email.",
	},
	"welcome": {
		"subject":         "Welcome to Base48, you're a member!",
		"heading":         "Welcome to Base48, you're a member! 🎉",
		"preheader":       "You're a Base48 member! Welcome aboard.",
		"intro_text":      "Your Base48 membership is now active. Welcome aboard.",
		"resources_text":  "1. JOIN THE CONVERSATION\n   Matrix: https://matrix.to/#/#base48:matrix.org\n\n2. KNOW THE RULES\n   → https://wiki.base48.cz/wiki/Rules\n\n3. EXPLORE THE WIKI\n   Tool guides, project docs, and everything else:\n   → https://wiki.base48.cz",
		"membership_text": "Your membership is active. Log in to the portal, check your variable symbol, and set your fee amount.",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Questions? Just reply to this email.",
	},
	"prepaid_ending": {
		"subject":          "Your Base48 membership is paid until the end of {zaplaceno_do}",
		"subject_short":    "Your Base48 membership is {chybi} short for {mesic}",
		"preheader":        "Thanks for your support! If you can, pay at least {mesice} ahead.",
		"preheader_short":  "Please top up {chybi} for {mesic}, ideally {mesice} ahead.",
		"heading":          "Thanks for supporting Base48",
		"heading_short":    "{chybi} short for {mesic}",
		"intro_text":       "thank you for paying your membership fees. Here is where you stand:",
		"intro_text_short": "your membership fee for {mesic} is still a little short. This is not a debt reminder, just a heads-up before it becomes one:",
		"prepay_text":      "The next fee is charged at the start of next month. If you can, pay at least {mesice} ahead: fewer payments for you, less matching for us, and the association knows what it can count on. Thank you!",
		"sign_off":         "Base48 hackerspace",
		"footer":           "Questions, or something does not add up? Just reply to this email.",
	},
	"negative_balance": {
		"subject":     "Negative membership fee balance",
		"heading":     "Negative membership fee balance",
		"preheader":   "Your membership fee balance is negative.",
		"intro_text":  "Your current membership fee balance is negative:",
		"action_text": "This means you owe Base48 for membership fees. Please make a payment as soon as possible.",
		"sign_off":    "Base48 hackerspace",
		"footer":      "If you have questions about your balance or have trouble with payment, please contact us.",
	},
	"debt_warning": {
		"subject":           "⚠️ Membership debt warning",
		"heading":           "⚠️ Membership debt warning",
		"preheader":         "Your debt has exceeded twice your monthly fee.",
		"warning_text":      "Your membership fee debt has exceeded twice your monthly fee.",
		"consequences_text": "If the debt is not settled soon, your membership may be suspended and access to the space restricted.",
		"steps_text":        "1. Pay the debt as soon as possible using the payment details below\n2. If you have financial difficulties, contact us. We can arrange installments or a fee reduction\n3. Check the portal to make sure all your payments have been correctly assigned",
		"sign_off":          "Base48 hackerspace",
		"footer":            "Need help? If you have questions or need to arrange an individual solution, don't hesitate to contact us. We're here to help.",
	},
	"membership_suspended": {
		"subject":           "Base48 membership suspended",
		"heading":           "Base48 membership suspended",
		"preheader":         "Your Base48 membership has been suspended.",
		"intro_text":        "Your Base48 membership has been suspended.",
		"consequences_text": "Access to the Base48 space is temporarily restricted. Membership benefits are suspended.",
		"recovery_text":     "Check your balance in the portal. If the reason is unpaid debt, please pay the amount as soon as possible.",
		"sign_off":          "Base48 hackerspace",
		"footer":            "If you believe this is a mistake, don't hesitate to contact us.",
	},
	"bar_debt": {
		"subject":     "Your Base48 bar tab is in debt",
		"heading":     "Your bar tab is in debt",
		"preheader":   "Your Base48 bar account is below zero.",
		"intro_text":  "Your account at the bar kiosk is below zero:",
		"action_text": "Please settle it on your next visit: at the kiosk, scan a top-up amount, then scan your card (or type your nickname) and put the cash in the till.",
		"sign_off":    "Base48 hackerspace",
		"footer":      "If the balance looks wrong, let us know. Just reply to this email.",
	},
}

// allDefaultContentBlocks maps lang code to the respective defaults map.
var allDefaultContentBlocks = map[string]map[string]map[string]string{
	"cs": defaultContentBlocks,
	"en": defaultContentBlocksEN,
}

// emailLabels returns structural labels for email templates in the given language.
// These are code-defined (not admin-editable) micro-texts like field labels and button text.
func EmailLabels(lang string) map[string]string {
	if lang == "en" {
		return map[string]string{
			"greeting_prefix":        "Hi",
			"credentials_heading":    "Your login credentials:",
			"username_label":         "Username:",
			"payment_heading":        "Payment details:",
			"account_label":          "Account number:",
			"iban_label":             "IBAN:",
			"vs_label":               "Variable symbol:",
			"payment_message_label":  "Payment reference:",
			"qr_instruction":         "Scan the QR code in your banking app. You can change the amount there.",
			"button_portal":          "Open Member Portal",
			"button_view_profile":    "View details in portal",
			"button_my_profile":      "View my profile",
			"reason_heading":         "Reason for suspension:",
			"current_debt_label":     "Current debt:",
			"monthly_fee_label":      "Monthly fee:",
			"paid_through_label":     "Paid until the end of",
			"short_label":            "Short for %s:",
			"amount_suggested_label": "Suggested payment:",
			"amount_minimum_label":   "At least:",
			"amount_note_debt":       "your debt and %s ahead, paid until the end of %s",
			"amount_note_short":      "what is missing and %s ahead, paid until the end of %s",
			"amount_note_ahead":      "%s ahead, paid until the end of %s",
			"vs_reminder":            "Important: Use your variable symbol (%s) so we can automatically match the payment to your account.",
			"bar_balance_label":      "Bar balance:",
			"bar_threshold_note":     "We send this reminder once your bar debt goes above %s CZK.",
		}
	}
	// Czech (default)
	return map[string]string{
		"greeting_prefix":        "Ahoj",
		"credentials_heading":    "Tvé přihlašovací údaje:",
		"username_label":         "Uživatelské jméno:",
		"payment_heading":        "Platební údaje:",
		"account_label":          "Číslo účtu:",
		"iban_label":             "IBAN:",
		"vs_label":               "Variabilní symbol:",
		"payment_message_label":  "Zpráva pro příjemce:",
		"qr_instruction":         "QR kód naskenuj v\u00a0bankovní aplikaci. Částku v\u00a0ní můžeš změnit.",
		"button_portal":          "Otevřít členský portál",
		"button_view_profile":    "Zobrazit detail v\u00a0portálu",
		"button_my_profile":      "Zobrazit můj profil",
		"reason_heading":         "Důvod pozastavení:",
		"current_debt_label":     "Aktuální dluh:",
		"monthly_fee_label":      "Měsíční příspěvek:",
		"paid_through_label":     "Zaplaceno do konce",
		"short_label":            "Na %s chybí:",
		"amount_suggested_label": "Doporučená platba:",
		"amount_minimum_label":   "Nejméně:",
		"amount_note_debt":       "dluh a\u00a0%s dopředu, zaplaceno pak do konce %s",
		"amount_note_short":      "doplatek a\u00a0%s dopředu, zaplaceno pak do konce %s",
		"amount_note_ahead":      "%s dopředu, zaplaceno pak do konce %s",
		"vs_reminder":            "Důležité: Použij svůj variabilní symbol (%s), ať můžeme platbu automaticky přiřadit k\u00a0tvému účtu.",
		"bar_balance_label":      "Zůstatek na baru:",
		"bar_threshold_note":     "Tuto upomínku posíláme, když dluh na baru stoupne nad %s\u00a0Kč.",
	}
}

// userLang is the language the user gets emails in, "cs" unless it is English.
func userLang(user *db.User) string {
	if NormalizeLocale(user.Locale) == "en" {
		return "en"
	}
	return "cs"
}

// NormalizeLocale maps a locale such as "en-US" or "cs_CZ" to one of the
// languages emails are written in, "cs" or "en", and anything else to "".
func NormalizeLocale(locale string) string {
	lang, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(locale)), "-")
	lang, _, _ = strings.Cut(lang, "_")
	switch lang {
	case "cs", "en":
		return lang
	}
	return ""
}

// GetDefaultContentBlocks returns the default content blocks for all templates in a given language.
// Used by the admin UI to show defaults when no DB overrides exist.
func GetDefaultContentBlocks(lang string) map[string]map[string]string {
	if defs, ok := allDefaultContentBlocks[lang]; ok {
		return defs
	}
	return defaultContentBlocks
}

// ErrEmailDisabled is what queueing and sending return while EMAIL_ENABLED is
// off, so that no caller reports an email that never reached the outbox.
var ErrEmailDisabled = errors.New("e-maily jsou vypnuté (EMAIL_ENABLED=false)")

// ErrBusy means another process holds the email: a worker is sending it.
var ErrBusy = errors.New("e-mail se právě odesílá")

// retryDelays is how long an email waits after each failed try before the
// next one, as SQLite modifiers. With max_attempts 3 a relay that is down
// gets ten minutes, then an hour, before the email is marked failed.
var retryDelays = []string{"+10 minutes", "+1 hour"}

// smtpTimeout bounds one whole SMTP conversation. It is well under the
// five-minute lease: a relay that stops answering must not keep an email
// past it, or another worker would claim the email and send it again.
const smtpTimeout = time.Minute

// QueueEmail renders an email and parks it in the outbox, due after the
// client's delay (or at the time set with At). Nothing is sent inline: the
// worker (ProcessPendingEmails) delivers it once due, after a last look. All
// email sending goes through this method.
func (c *Client) QueueEmail(ctx context.Context, params SendParams) error {
	if !c.config.EmailEnabled {
		log.Printf("[Email] Email disabled (EMAIL_ENABLED=false), not queued: %s -> %s", params.TemplateName, params.Recipient)
		return ErrEmailDisabled
	}

	rendered, err := c.renderTemplate(params)
	if err != nil {
		c.logEmail(ctx, params, fmt.Errorf("render failed: %w", err))
		return fmt.Errorf("failed to render template: %w", err)
	}

	// The data goes along so a reminder can be checked again before it leaves.
	dataJSON, _ := json.Marshal(params.Data)

	// UTC matters: the driver serializes a time.Time with its own location's
	// wall clock, while every due-check in SQL compares against datetime('now'),
	// which is UTC. Storing local time made every scheduled email fire TZ-offset
	// hours late (2h in CEST).
	due := c.dueAt
	if due.IsZero() {
		due = time.Now().Add(c.DefaultDelay)
	}

	entry, err := c.queries.CreateEmailOutbox(ctx, db.CreateEmailOutboxParams{
		UserID:       params.UserID,
		Recipient:    params.Recipient,
		Subject:      params.Subject,
		TemplateName: params.TemplateName,
		TemplateData: sql.NullString{String: string(dataJSON), Valid: true},
		RenderedHtml: sql.NullString{String: rendered, Valid: true},
		Status:       "pending",
		MaxAttempts:  3,
		NextRetryAt:  sql.NullTime{Time: due.UTC(), Valid: true},
	})
	if err != nil {
		c.logEmail(ctx, params, fmt.Errorf("outbox insert failed: %w", err))
		return fmt.Errorf("failed to create outbox entry: %w", err)
	}

	log.Printf("[Email] Queued outbox #%d, due %s: %s -> %s",
		entry.ID, due.Local().Format("2006-01-02 15:04"), params.TemplateName, params.Recipient)
	return nil
}

// SendTemplated renders and sends an email straight away, outside the outbox
// and regardless of EMAIL_ENABLED. Only for test emails, which go to the admin
// who asked for them.
func (c *Client) SendTemplated(ctx context.Context, params SendParams) error {
	if c.config.SMTPHost == "" {
		return errors.New("SMTP není nastavené (SMTP_HOST)")
	}

	rendered, err := c.renderTemplate(params)
	if err != nil {
		return c.logEmail(ctx, params, fmt.Errorf("template error: %w", err))
	}

	err = c.sendSMTP(params.Recipient, params.Subject, rendered)
	return c.logEmail(ctx, params, err)
}

// checkSMTP does a quick TCP connectivity check to the SMTP server.
// Returns nil if reachable, error otherwise.
func (c *Client) checkSMTP() error {
	if c.config.SMTPHost == "" {
		return errors.New("SMTP není nastavené (SMTP_HOST)")
	}
	addr := net.JoinHostPort(c.config.SMTPHost, strconv.Itoa(c.config.SMTPPort))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("SMTP server %s je nedostupný: %w", addr, err)
	}
	conn.Close()
	return nil
}

// canDeliver says why an admin's send-now or retry cannot go out right now.
func (c *Client) canDeliver() error {
	if !c.config.EmailEnabled {
		return ErrEmailDisabled
	}
	return c.checkSMTP()
}

// RetryEmail puts a failed email back in the queue and sends it now.
func (c *Client) RetryEmail(ctx context.Context, outboxID int64) error {
	if err := c.canDeliver(); err != nil {
		return err
	}
	if _, err := c.queries.RequeueFailedEmail(ctx, outboxID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("znovu lze poslat jen e-mail, jehož odeslání selhalo")
	} else if err != nil {
		return fmt.Errorf("vrácení do fronty selhalo: %w", err)
	}
	return c.sendClaimed(ctx, outboxID)
}

// SendNow delivers a pending email without waiting for its time.
func (c *Client) SendNow(ctx context.Context, outboxID int64) error {
	if err := c.canDeliver(); err != nil {
		return err
	}
	return c.sendClaimed(ctx, outboxID)
}

// sendClaimed leases one pending email and delivers it through the same path
// as the worker, last look included: a reminder for a member who has paid
// since is withdrawn, not sent.
func (c *Client) sendClaimed(ctx context.Context, outboxID int64) error {
	entry, err := c.queries.ClaimEmailOutbox(ctx, outboxID)
	if errors.Is(err, sql.ErrNoRows) {
		current, getErr := c.queries.GetEmailOutbox(ctx, outboxID)
		if getErr != nil {
			return fmt.Errorf("e-mail #%d neexistuje", outboxID)
		}
		if current.Status == "pending" {
			return ErrBusy
		}
		return fmt.Errorf("e-mail už nečeká na odeslání (stav %s)", current.Status)
	}
	if err != nil {
		return fmt.Errorf("e-mail #%d nejde zamknout: %w", outboxID, err)
	}

	switch outcome, err := c.deliver(ctx, &entry); outcome {
	case delivered:
		return nil
	case withdrawn:
		return fmt.Errorf("zrušeno před odesláním: %s", strings.TrimPrefix(entry.LastError.String, cancelledPrefix))
	case held:
		return errors.New("teď ho nejde ověřit, odešle se samo během několika minut")
	default:
		return err
	}
}

// ProcessPendingEmails sends scheduled emails whose delivery time has arrived.
// Runs on every sync tick of the cron daemon (every 2 min) and right after an
// admin queues reminders with a zero timer. Returns how many went out.
//
// Entries are claimed with a lease rather than merely listed: the server and the
// cron daemon are separate processes against the same SQLite file, so a plain
// list-then-send would let both grab the same row and mail a member twice.
func (c *Client) ProcessPendingEmails(ctx context.Context) int {
	// Pending rows in a copy of the production database must not reach real
	// members from a test instance, so the switch stops the worker too.
	if !c.config.EmailEnabled || c.config.SMTPHost == "" {
		return 0
	}

	entries, err := c.queries.ClaimScheduledEmails(ctx)
	if err != nil {
		log.Printf("[Email] Failed to claim scheduled emails: %v", err)
		return 0
	}

	sent := 0
	for _, entry := range entries {
		if outcome, _ := c.deliver(ctx, &entry); outcome == delivered {
			sent++
		}
	}

	if sent > 0 {
		log.Printf("[Email] Sent %d scheduled emails", sent)
	}
	return sent
}

// outcome is what became of one leased email.
type outcome int

const (
	delivered outcome = iota
	withdrawn         // cancelled by the last look
	held              // the last look could not be made; the lease brings it back
	retrying          // the try failed, another one is scheduled
	failed            // the last try failed
)

// deliver gives a leased email its last look, then one SMTP try. A failed try
// is rescheduled while tries are left (see retryDelays); after the last one the
// email is marked failed. The error says why it did not go out.
func (c *Client) deliver(ctx context.Context, entry *db.EmailOutbox) (outcome, error) {
	// The request that asked for a send-now may end before the SMTP server
	// answers; the outcome must be recorded all the same.
	dbCtx := context.WithoutCancel(ctx)

	switch c.refreshBeforeSend(dbCtx, entry) {
	case lookCancelled:
		return withdrawn, nil
	case lookHeld:
		return held, nil
	}

	params := SendParams{
		UserID:       entry.UserID,
		Recipient:    entry.Recipient,
		Subject:      entry.Subject,
		TemplateName: entry.TemplateName,
	}
	attempts := entry.Attempts + 1

	sendErr := c.sendSMTP(entry.Recipient, entry.Subject, entry.RenderedHtml.String)
	if sendErr == nil {
		if err := c.queries.MarkEmailSent(dbCtx, db.MarkEmailSentParams{Attempts: attempts, ID: entry.ID}); err != nil {
			log.Printf("[Email] outbox #%d went out but could not be marked sent: %v", entry.ID, err)
		}
		c.logEmail(dbCtx, params, nil)
		return delivered, nil
	}

	log.Printf("[Email] Try %d/%d of outbox #%d to %s failed: %v", attempts, entry.MaxAttempts, entry.ID, entry.Recipient, sendErr)
	lastError := sql.NullString{String: sendErr.Error(), Valid: true}
	if attempts < entry.MaxAttempts && int(attempts) <= len(retryDelays) {
		delay := retryDelays[attempts-1]
		if err := c.queries.RescheduleEmail(dbCtx, db.RescheduleEmailParams{
			Attempts: attempts, LastError: lastError, Delay: delay, ID: entry.ID,
		}); err != nil {
			log.Printf("[Email] failed to reschedule outbox #%d: %v", entry.ID, err)
		}
		return retrying, fmt.Errorf("odeslání selhalo, další pokus za %s: %w", strings.TrimPrefix(delay, "+"), sendErr)
	}

	if err := c.queries.MarkEmailFailed(dbCtx, db.MarkEmailFailedParams{
		Attempts: attempts, LastError: lastError, ID: entry.ID,
	}); err != nil {
		log.Printf("[Email] failed to mark outbox #%d failed: %v", entry.ID, err)
	}
	deliveryErr := fmt.Errorf("odeslání selhalo i na %d. pokus: %w", attempts, sendErr)
	c.logEmail(dbCtx, params, deliveryErr)
	return failed, deliveryErr
}

// sendSMTP sends a pre-rendered email via SMTP. STARTTLS is used whenever the
// server offers it. When SMTPSkipTLS is set (a local relay such as Postfix),
// its certificate is not checked, a failed STARTTLS is not fatal, and AUTH is
// used only with credentials and only if offered; otherwise authentication is
// required, as net/smtp.SendMail does it.
func (c *Client) sendSMTP(recipient, subject, htmlBody string) error {
	addr := net.JoinHostPort(c.config.SMTPHost, strconv.Itoa(c.config.SMTPPort))
	message := c.formatMessage(recipient, subject, htmlBody)

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(smtpTimeout)); err != nil {
		return fmt.Errorf("smtp deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, c.config.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{ServerName: c.config.SMTPHost}
		if c.config.SMTPSkipTLS {
			tlsConfig.InsecureSkipVerify = true
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			if !c.config.SMTPSkipTLS {
				return fmt.Errorf("smtp STARTTLS: %w", err)
			}
			log.Printf("[Email] STARTTLS failed (continuing without TLS): %v", err)
		}
	}

	hasAuth, _ := client.Extension("AUTH")
	switch {
	case !c.config.SMTPSkipTLS && !hasAuth:
		return errors.New("smtp: server doesn't support AUTH")
	case !c.config.SMTPSkipTLS || (c.config.SMTPUsername != "" && hasAuth):
		auth := smtp.PlainAuth("", c.config.SMTPUsername, c.config.SMTPPassword, c.config.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	case c.config.SMTPUsername != "":
		log.Printf("[Email] Server does not advertise AUTH, skipping authentication")
	}

	if err := client.Mail(c.config.SMTPFrom); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write([]byte(message)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}

	return client.Quit()
}

// layoutFile holds what every email template shares (see its comment).
const layoutFile = "_layout.html"

// renderTemplate renders an email template and returns the HTML string
func (c *Client) renderTemplate(params SendParams) (string, error) {
	dir := filepath.Join(c.config.WebRoot, "templates", "email")
	// The email first: ParseFiles names the set after it, and that is what
	// Execute runs. The layout brings the frame and the payment box.
	tmpl, err := template.ParseFiles(filepath.Join(dir, params.TemplateName), filepath.Join(dir, layoutFile))
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, params.Data); err != nil {
		return "", fmt.Errorf("template execution error: %w", err)
	}

	// Convert Unicode NBSP (from fixCzechTypography) to HTML &nbsp; entities.
	// This makes them visible in source view and universally supported by email clients.
	html := strings.ReplaceAll(body.String(), "\u00a0", "&nbsp;")
	return html, nil
}

// RenderPreview renders a template with data and returns the HTML.
// Public wrapper for admin preview endpoint.
func (c *Client) RenderPreview(params SendParams) (string, error) {
	return c.renderTemplate(params)
}

// formatMessage builds the RFC 5322 message handed to the SMTP server.
//
// Header values that can carry non-ASCII — the subject and the From display
// name — MUST be MIME encoded (RFC 2047). Raw UTF-8 in a header makes Postfix
// mark the message as requiring SMTPUTF8, and every server that does not
// advertise that extension then rejects it permanently with dsn=5.6.7.
// Seznam (seznam.cz, email.cz) and Protonmail are among them, so mail to a
// large part of the membership silently bounced. mime.QEncoding leaves
// pure-ASCII values untouched, so English subjects stay human-readable on the
// wire.
func (c *Client) formatMessage(to, subject, body string) string {
	from := c.config.SMTPFrom
	if c.config.SMTPFromName != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", c.config.SMTPFromName), c.config.SMTPFrom)
	}

	headers := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit",
		from, to, mime.QEncoding.Encode("utf-8", subject),
	)
	if c.config.SMTPReplyTo != "" {
		headers += fmt.Sprintf("\r\nReply-To: %s", c.config.SMTPReplyTo)
	}

	return headers + "\r\n\r\n" + body
}

// logEmail logs the email attempt to database
func (c *Client) logEmail(ctx context.Context, params SendParams, err error) error {
	level := "success"
	message := fmt.Sprintf("Email sent to %s: %s", params.Recipient, params.Subject)
	// Marshalled, not formatted: a quote in an address, a subject or an
	// SMTP error would otherwise end the JSON string and add fields.
	fields := map[string]string{"recipient": params.Recipient, "subject": params.Subject, "template": params.TemplateName}

	if err != nil {
		level = "error"
		message = fmt.Sprintf("Failed to send email to %s: %v", params.Recipient, err)
		fields["error"] = err.Error()
		log.Printf("[Email] %s", message)
	} else {
		log.Printf("[Email] %s", message)
	}

	metadata, _ := json.Marshal(fields)

	if c.queries != nil {
		if _, dbErr := c.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "email",
			Level:     level,
			UserID:    params.UserID,
			Message:   message,
			Metadata:  sql.NullString{String: string(metadata), Valid: true},
		}); dbErr != nil {
			log.Printf("[Email] Warning: failed to log to database: %v", dbErr)
		}
	}

	return err
}

// LoadContentBlocks loads template content overrides from DB, falling back to
// defaults, ready for rendering (Czech typography applied). lang should be
// "cs" or "en".
func (c *Client) LoadContentBlocks(ctx context.Context, templateName string, lang string) map[string]string {
	if lang == "" {
		lang = "cs"
	}
	result := c.LoadRawContentBlocks(ctx, templateName, lang)

	// Apply Czech typographic rules only for Czech
	if lang == "cs" {
		for k, v := range result {
			result[k] = fixCzechTypography(v)
		}
	}

	return result
}

// LoadRawContentBlocks is LoadContentBlocks without the typography pass: the
// text as an admin wrote it, which is what the template editor shows and
// compares against the defaults. Non-breaking spaces are folded back to plain
// ones — they are typography, re-added at render time, and older saves stored
// them, which made untouched blocks look edited.
func (c *Client) LoadRawContentBlocks(ctx context.Context, templateName string, lang string) map[string]string {
	defaults, ok := GetDefaultContentBlocks(lang)[templateName]
	if !ok {
		return map[string]string{}
	}

	result := make(map[string]string, len(defaults))
	for k, v := range defaults {
		result[k] = v
	}

	overrides, err := c.queries.ListEmailTemplateContentByTemplateLang(ctx, db.ListEmailTemplateContentByTemplateLangParams{
		TemplateName: templateName,
		Lang:         lang,
	})
	if err != nil {
		log.Printf("[Email] Warning: failed to load template content for %s/%s: %v", templateName, lang, err)
		return result
	}

	for _, override := range overrides {
		result[override.BlockName] = NormalizeBlockText(override.Content)
	}
	return result
}

// NormalizeBlockText folds non-breaking spaces to plain spaces, so editor
// input compares cleanly against the defaults.
func NormalizeBlockText(s string) string {
	return strings.ReplaceAll(s, "\u00a0", " ")
}

// displayName is how emails address the member: see db.User.DisplayName.
func displayName(user *db.User) string {
	return user.DisplayName()
}

// SendWelcome sends welcome email to newly accepted member
func (c *Client) SendWelcome(ctx context.Context, user *db.User) error {
	lang := userLang(user)
	content := c.LoadContentBlocks(ctx, "welcome", lang)
	labels := EmailLabels(lang)

	data := map[string]interface{}{
		"Name":      displayName(user),
		"Username":  user.Username.String,
		"PortalURL": c.config.BaseURL,
		"Content":   content,
		"Labels":    labels,
	}

	return c.QueueEmail(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      content["subject"],
		TemplateName: "welcome.html",
		Data:         data,
	})
}

// SendRegistration sends registration confirmation to newly created user (state="awaiting")
func (c *Client) SendRegistration(ctx context.Context, user *db.User) error {
	lang := userLang(user)
	content := c.LoadContentBlocks(ctx, "registration", lang)
	labels := EmailLabels(lang)

	data := map[string]interface{}{
		"Name":      displayName(user),
		"Username":  user.Username.String,
		"PortalURL": c.config.BaseURL,
		"Content":   content,
		"Labels":    labels,
	}

	return c.QueueEmail(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      content["subject"],
		TemplateName: "registration.html",
		Data:         data,
	})
}

// SendMembershipSuspended sends notification about membership suspension
func (c *Client) SendMembershipSuspended(ctx context.Context, user *db.User, reason string) error {
	lang := userLang(user)
	content := c.LoadContentBlocks(ctx, "membership_suspended", lang)
	labels := EmailLabels(lang)

	data := map[string]interface{}{
		"Name":      displayName(user),
		"Reason":    reason,
		"PortalURL": c.config.BaseURL,
		"Content":   content,
		"Labels":    labels,
	}

	return c.QueueEmail(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      content["subject"],
		TemplateName: "membership_suspended.html",
		Data:         data,
	})
}
