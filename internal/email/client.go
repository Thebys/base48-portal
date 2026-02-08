package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net"
	"net/smtp"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/qrpay"
)

// Client handles email sending with templates, outbox, and logging
type Client struct {
	config       *config.Config
	queries      *db.Queries
	qrpayService *qrpay.Service
	DefaultDelay time.Duration // If set, emails are scheduled instead of sent immediately
}

// SendParams contains parameters for sending a templated email
type SendParams struct {
	UserID       sql.NullInt64
	Recipient    string
	Subject      string
	TemplateName string
	Data         interface{}
}

// New creates a new email client
func New(cfg *config.Config, queries *db.Queries, qrService *qrpay.Service) *Client {
	return &Client{
		config:       cfg,
		queries:      queries,
		qrpayService: qrService,
	}
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
		"next_steps_text": "Co bude dál: tvé členství bude schváleno na nejbližším setkání komunity. Jakmile budeš přijat/a, pošleme ti uvítací e-mail s dalšími informacemi — obvykle to trvá týden nebo dva.",
		"wiki_text":       "Mezitím se můžeš podívat na naši wiki:\n→ https://wiki.base48.cz",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Máš otázky? Stačí odpovědět na tento e-mail.",
	},
	"welcome": {
		"subject":         "Vítej v Base48 — jsi členem!",
		"heading":         "Vítej v Base48 — jsi členem! 🎉",
		"preheader":       "Jsi členem Base48! Vítej na palubě.",
		"intro_text":      "Tvé členství v Base48 je aktivní. Vítej na palubě.",
		"resources_text":  "1. PŘIPOJ SE KE KONVERZACI\n   Matrix: https://matrix.to/#/#base48:matrix.org\n\n2. PŘEČTI SI PRAVIDLA\n   → https://wiki.base48.cz/wiki/Rules\n\n3. PROZKOUMEJ WIKI\n   Návody k nástrojům, dokumentace projektů a vše ostatní:\n   → https://wiki.base48.cz",
		"membership_text": "Tvé členství je aktivní. Přihlas se do portálu, zkontroluj svůj variabilní symbol a nastav si výši příspěvku.",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Máš otázky? Stačí odpovědět na tento e-mail.",
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
}

// Default content blocks — English
var defaultContentBlocksEN = map[string]map[string]string{
	"registration": {
		"subject":         "Your Base48 account is ready",
		"heading":         "Your account is ready",
		"preheader":       "Your Base48 portal account is ready.",
		"intro_text":      "Your account on the Base48 member portal is set up.",
		"next_steps_text": "What happens next: your membership will be reviewed at our next community meeting. We'll email you as soon as you're approved — usually within a week or two.",
		"wiki_text":       "While you wait, check out our wiki:\n→ https://wiki.base48.cz",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Questions? Just reply to this email.",
	},
	"welcome": {
		"subject":         "Welcome to Base48 — you're a member!",
		"heading":         "Welcome to Base48 — you're a member! 🎉",
		"preheader":       "You're a Base48 member! Welcome aboard.",
		"intro_text":      "Your Base48 membership is now active. Welcome aboard.",
		"resources_text":  "1. JOIN THE CONVERSATION\n   Matrix: https://matrix.to/#/#base48:matrix.org\n\n2. KNOW THE RULES\n   → https://wiki.base48.cz/wiki/Rules\n\n3. EXPLORE THE WIKI\n   Tool guides, project docs, and everything else:\n   → https://wiki.base48.cz",
		"membership_text": "Your membership is active. Log in to the portal, check your variable symbol, and set your fee amount.",
		"sign_off":        "Base48 hackerspace",
		"footer":          "Questions? Just reply to this email.",
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
		"steps_text":        "1. Pay the debt as soon as possible using the payment details below\n2. If you have financial difficulties, contact us — we can arrange installments or a fee reduction\n3. Check the portal to make sure all your payments have been correctly assigned",
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
			"greeting_prefix":      "Hi",
			"credentials_heading":  "Your login credentials:",
			"username_label":       "Username:",
			"payment_heading":      "Payment details:",
			"account_label":        "Account number:",
			"vs_label":             "Variable symbol:",
			"payment_message_label": "Payment reference:",
			"payment_ref_text":     "Base48 membership fee",
			"qr_instruction":       "Scan QR code in your banking app",
			"button_portal":        "Open Member Portal",
			"button_wiki":          "Browse the wiki",
			"button_view_profile":  "View details in portal",
			"button_my_profile":    "View my profile",
			"reason_heading":       "Reason for suspension:",
			"amount_due_label":     "Amount due:",
			"current_debt_label":   "Current debt:",
			"monthly_fee_label":    "Monthly fee:",
			"or_partial":           "(or at least a portion)",
			"bank_name":            "Fio bank",
			"vs_reminder":          "Important: Use your variable symbol (%s) so we can automatically match the payment to your account.",
			"debt_payment_ref":     "Membership fee payment",
		}
	}
	// Czech (default)
	return map[string]string{
		"greeting_prefix":      "Ahoj",
		"credentials_heading":  "Tvé přihlašovací údaje:",
		"username_label":       "Uživatelské jméno:",
		"payment_heading":      "Platební údaje:",
		"account_label":        "Číslo účtu:",
		"vs_label":             "Variabilní symbol:",
		"payment_message_label": "Zpráva pro příjemce:",
		"payment_ref_text":     "Členský příspěvek Base48",
		"qr_instruction":       "Naskenuj QR kód v\u00a0bankovní aplikaci",
		"button_portal":        "Otevřít členský portál",
		"button_wiki":          "Prohlédnout wiki",
		"button_view_profile":  "Zobrazit detail v\u00a0portálu",
		"button_my_profile":    "Zobrazit můj profil",
		"reason_heading":       "Důvod pozastavení:",
		"amount_due_label":     "Částka k\u00a0úhradě:",
		"current_debt_label":   "Aktuální dluh:",
		"monthly_fee_label":    "Měsíční příspěvek:",
		"or_partial":           "(nebo alespoň část)",
		"bank_name":            "Fio banka",
		"vs_reminder":          "Důležité: Použij svůj variabilní symbol (%s), ať můžeme platbu automaticky přiřadit k\u00a0tvému účtu.",
		"debt_payment_ref":     "Úhrada členského příspěvku",
	}
}

// userLang returns the user's locale, defaulting to "cs".
func userLang(user *db.User) string {
	if user.Locale != "" && (user.Locale == "cs" || user.Locale == "en") {
		return user.Locale
	}
	return "cs"
}

// GetDefaultContentBlocks returns the default content blocks for all templates in a given language.
// Used by the admin UI to show defaults when no DB overrides exist.
func GetDefaultContentBlocks(lang string) map[string]map[string]string {
	if defs, ok := allDefaultContentBlocks[lang]; ok {
		return defs
	}
	return defaultContentBlocks
}

// QueueEmail creates an outbox entry and attempts immediate delivery with retry.
// If DefaultDelay is set, the email is scheduled for later delivery instead.
// All email sending should go through this method.
func (c *Client) QueueEmail(ctx context.Context, params SendParams) error {
	if !c.config.EmailEnabled {
		log.Printf("[Email] Email disabled (EMAIL_ENABLED=false), skipping: %s -> %s", params.TemplateName, params.Recipient)
		return nil
	}

	// Render the template to get preview HTML
	rendered, err := c.renderTemplate(params)
	if err != nil {
		c.logEmail(ctx, params, fmt.Errorf("render failed: %w", err))
		return fmt.Errorf("failed to render template: %w", err)
	}

	// Serialize template data to JSON for re-rendering capability
	dataJSON, _ := json.Marshal(params.Data)

	// Schedule for later if delay is set
	var nextRetryAt sql.NullTime
	if c.DefaultDelay > 0 {
		nextRetryAt = sql.NullTime{Time: time.Now().Add(c.DefaultDelay), Valid: true}
	}

	// Insert into outbox
	outboxEntry, err := c.queries.CreateEmailOutbox(ctx, db.CreateEmailOutboxParams{
		UserID:       params.UserID,
		Recipient:    params.Recipient,
		Subject:      params.Subject,
		TemplateName: params.TemplateName,
		TemplateData: sql.NullString{String: string(dataJSON), Valid: true},
		RenderedHtml: sql.NullString{String: rendered, Valid: true},
		Status:       "pending",
		MaxAttempts:  3,
		NextRetryAt:  nextRetryAt,
	})
	if err != nil {
		c.logEmail(ctx, params, fmt.Errorf("outbox insert failed: %w", err))
		return fmt.Errorf("failed to create outbox entry: %w", err)
	}

	// If delayed, don't send now — will be picked up by ProcessPendingEmails
	if c.DefaultDelay > 0 {
		log.Printf("[Email] Scheduled for delivery in %v (outbox #%d): %s -> %s",
			c.DefaultDelay, outboxEntry.ID, params.TemplateName, params.Recipient)
		return nil
	}

	// Skip SMTP if not configured
	if c.config.SMTPHost == "" {
		log.Printf("[Email] SMTP not configured, email queued but not sent (outbox #%d)", outboxEntry.ID)
		return nil
	}

	// Attempt immediate delivery with retry
	c.attemptDelivery(ctx, outboxEntry, params)

	return nil
}

// SendTemplated sends an email directly (bypasses outbox).
// Used for test emails from admin settings.
func (c *Client) SendTemplated(ctx context.Context, params SendParams) error {
	if c.config.SMTPHost == "" {
		log.Printf("[Email] SMTP not configured, skipping email to %s (template: %s)", params.Recipient, params.TemplateName)
		return nil
	}

	rendered, err := c.renderTemplate(params)
	if err != nil {
		return c.logEmail(ctx, params, fmt.Errorf("template error: %w", err))
	}

	err = c.sendSMTP(params.Recipient, params.Subject, rendered)
	return c.logEmail(ctx, params, err)
}

// RetryEmail retries a failed outbox entry.
func (c *Client) RetryEmail(ctx context.Context, outboxID int64) error {
	entry, err := c.queries.GetEmailOutbox(ctx, outboxID)
	if err != nil {
		return fmt.Errorf("outbox entry not found: %w", err)
	}
	if entry.Status != "failed" {
		return fmt.Errorf("can only retry failed emails, current status: %s", entry.Status)
	}

	// Reset to pending for retry
	entry, err = c.queries.UpdateEmailOutboxStatus(ctx, db.UpdateEmailOutboxStatusParams{
		Status:   "pending",
		Attempts: 0,
		ID:       entry.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to reset outbox entry: %w", err)
	}

	params := SendParams{
		UserID:       entry.UserID,
		Recipient:    entry.Recipient,
		Subject:      entry.Subject,
		TemplateName: entry.TemplateName,
	}

	c.attemptDelivery(ctx, entry, params)
	return nil
}

// SendNow forces immediate delivery of a pending scheduled email.
func (c *Client) SendNow(ctx context.Context, outboxID int64) error {
	entry, err := c.queries.GetEmailOutbox(ctx, outboxID)
	if err != nil {
		return fmt.Errorf("outbox entry not found: %w", err)
	}
	if entry.Status != "pending" {
		return fmt.Errorf("can only send pending emails, current status: %s", entry.Status)
	}

	// Clear schedule and reset attempts
	entry, err = c.queries.UpdateEmailOutboxStatus(ctx, db.UpdateEmailOutboxStatusParams{
		Status:   "pending",
		Attempts: 0,
		ID:       entry.ID,
	})
	if err != nil {
		return fmt.Errorf("failed to reset outbox entry: %w", err)
	}

	params := SendParams{
		UserID:       entry.UserID,
		Recipient:    entry.Recipient,
		Subject:      entry.Subject,
		TemplateName: entry.TemplateName,
	}

	c.attemptDelivery(ctx, entry, params)
	return nil
}

// ProcessPendingEmails sends scheduled emails whose delivery time has arrived.
// Called from sync_fio_payments (runs every 2 min) to deliver delayed emails.
func (c *Client) ProcessPendingEmails(ctx context.Context) int {
	if c.config.SMTPHost == "" {
		return 0
	}

	entries, err := c.queries.ListPendingScheduledEmails(ctx)
	if err != nil {
		log.Printf("[Email] Failed to list scheduled emails: %v", err)
		return 0
	}

	sent := 0
	for _, entry := range entries {
		params := SendParams{
			UserID:       entry.UserID,
			Recipient:    entry.Recipient,
			Subject:      entry.Subject,
			TemplateName: entry.TemplateName,
		}
		c.attemptDelivery(ctx, entry, params)
		sent++
	}

	if sent > 0 {
		log.Printf("[Email] Processed %d scheduled emails", sent)
	}
	return sent
}

// attemptDelivery tries to send an email with exponential backoff retry.
// Backoff schedule: attempt 1 = immediate, attempt 2 = 1s, attempt 3 = 5s
func (c *Client) attemptDelivery(ctx context.Context, entry db.EmailOutbox, params SendParams) {
	backoffDurations := []time.Duration{0, 1 * time.Second, 5 * time.Second}

	for attempt := 0; attempt < int(entry.MaxAttempts); attempt++ {
		if attempt > 0 && attempt < len(backoffDurations) {
			time.Sleep(backoffDurations[attempt])
		}

		err := c.sendSMTP(entry.Recipient, entry.Subject, entry.RenderedHtml.String)

		if err == nil {
			// Success
			now := time.Now()
			c.queries.UpdateEmailOutboxStatus(ctx, db.UpdateEmailOutboxStatusParams{
				Status:   "sent",
				Attempts: int64(attempt + 1),
				SentAt:   sql.NullTime{Time: now, Valid: true},
				ID:       entry.ID,
			})
			c.logEmail(ctx, params, nil)
			return
		}

		log.Printf("[Email] Attempt %d/%d failed for %s: %v", attempt+1, entry.MaxAttempts, entry.Recipient, err)
	}

	// All attempts exhausted
	c.queries.UpdateEmailOutboxStatus(ctx, db.UpdateEmailOutboxStatusParams{
		Status:    "failed",
		Attempts:  entry.MaxAttempts,
		LastError: sql.NullString{String: "max attempts exhausted", Valid: true},
		ID:        entry.ID,
	})
	c.logEmail(ctx, params, fmt.Errorf("failed after %d attempts", entry.MaxAttempts))
}

// sendSMTP sends a pre-rendered email via SMTP.
// When SMTPSkipTLS is set, it connects without STARTTLS and skips authentication
// (suitable for local relays like Postfix that don't require TLS or auth).
func (c *Client) sendSMTP(recipient, subject, htmlBody string) error {
	addr := fmt.Sprintf("%s:%d", c.config.SMTPHost, c.config.SMTPPort)
	message := c.formatMessage(recipient, subject, htmlBody)

	if !c.config.SMTPSkipTLS {
		auth := smtp.PlainAuth("", c.config.SMTPUsername, c.config.SMTPPassword, c.config.SMTPHost)
		return smtp.SendMail(addr, auth, c.config.SMTPFrom, []string{recipient}, []byte(message))
	}

	// Plain TCP connection without TLS — for local relays
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, c.config.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	// Try STARTTLS with InsecureSkipVerify — if the server supports it, use it;
	// if not, continue without TLS.
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{InsecureSkipVerify: true}); err != nil {
			log.Printf("[Email] STARTTLS failed (continuing without TLS): %v", err)
		}
	}

	// Authenticate only if credentials are provided and server supports it
	if c.config.SMTPUsername != "" {
		if ok, _ := client.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", c.config.SMTPUsername, c.config.SMTPPassword, c.config.SMTPHost)
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		} else {
			log.Printf("[Email] Server does not advertise AUTH, skipping authentication")
		}
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

// renderTemplate renders an email template and returns the HTML string
func (c *Client) renderTemplate(params SendParams) (string, error) {
	templatePath := filepath.Join(c.config.WebRoot, "templates", "email", params.TemplateName)
	tmpl, err := template.ParseFiles(templatePath)
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

// formatMessage creates RFC 2822 compliant email message
func (c *Client) formatMessage(to, subject, body string) string {
	from := c.config.SMTPFrom
	if c.config.SMTPFromName != "" {
		from = fmt.Sprintf("%s <%s>", c.config.SMTPFromName, c.config.SMTPFrom)
	}

	headers := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8",
		from, to, subject,
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
	metadata := fmt.Sprintf(`{"recipient":"%s","subject":"%s","template":"%s"}`,
		params.Recipient, params.Subject, params.TemplateName)

	if err != nil {
		level = "error"
		message = fmt.Sprintf("Failed to send email to %s: %v", params.Recipient, err)
		metadata = fmt.Sprintf(`{"recipient":"%s","subject":"%s","template":"%s","error":"%s"}`,
			params.Recipient, params.Subject, params.TemplateName, err.Error())
		log.Printf("[Email] %s", message)
	} else {
		log.Printf("[Email] %s", message)
	}

	if c.queries != nil {
		if _, dbErr := c.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "email",
			Level:     level,
			UserID:    params.UserID,
			Message:   message,
			Metadata:  sql.NullString{String: metadata, Valid: true},
		}); dbErr != nil {
			log.Printf("[Email] Warning: failed to log to database: %v", dbErr)
		}
	}

	return err
}

// generateEmailQR builds a URL to the /api/qr endpoint for embedding in emails.
// Uses max(abs(debtAmount), minAmount) so the QR is never for less than minAmount.
// Returns empty string if QR service is not available or user has no PaymentsID.
func (c *Client) generateEmailQR(user *db.User, debtAmount float64, minAmount float64) string {
	if c.qrpayService == nil || !c.qrpayService.IsConfigured() {
		return ""
	}
	if !user.PaymentsID.Valid || user.PaymentsID.String == "" {
		return ""
	}

	amount := math.Max(math.Abs(debtAmount), minAmount)
	return fmt.Sprintf("%s/api/qr?vs=%s&amount=%.0f", c.config.BaseURL, user.PaymentsID.String, amount)
}

// GenerateQRForEmail builds a URL to the /api/qr endpoint.
// Intended for test/preview handlers that need to attach QR to template data.
func (c *Client) GenerateQRForEmail(vs string, amount float64) string {
	if c.qrpayService == nil || !c.qrpayService.IsConfigured() {
		return ""
	}
	if vs == "" {
		return ""
	}

	return fmt.Sprintf("%s/api/qr?vs=%s&amount=%.0f", c.config.BaseURL, vs, amount)
}

// LoadContentBlocks loads template content overrides from DB, falling back to defaults.
// lang should be "cs" or "en".
func (c *Client) LoadContentBlocks(ctx context.Context, templateName string, lang string) map[string]string {
	if lang == "" {
		lang = "cs"
	}
	defs := GetDefaultContentBlocks(lang)
	defaults, ok := defs[templateName]
	if !ok {
		return map[string]string{}
	}

	result := make(map[string]string)
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
		result[override.BlockName] = override.Content
	}

	// Apply Czech typographic rules only for Czech
	if lang == "cs" {
		for k, v := range result {
			result[k] = fixCzechTypography(v)
		}
	}

	return result
}

// displayName returns the user's nickname (Username) with fallback to Realname, then email.
func displayName(user *db.User) string {
	if user.Username.Valid && user.Username.String != "" {
		return user.Username.String
	}
	if user.Realname.Valid && user.Realname.String != "" {
		return user.Realname.String
	}
	return user.Email
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

// SendNegativeBalance sends notification about negative membership balance.
// minQRAmount sets the floor for the QR code amount (typically the user's monthly fee).
func (c *Client) SendNegativeBalance(ctx context.Context, user *db.User, balance float64, minQRAmount float64) error {
	lang := userLang(user)
	content := c.LoadContentBlocks(ctx, "negative_balance", lang)
	labels := EmailLabels(lang)

	data := map[string]interface{}{
		"Name":          displayName(user),
		"Balance":       balance,
		"AbsBalance":    math.Abs(balance),
		"PaymentsID":    user.PaymentsID.String,
		"PortalURL":     c.config.BaseURL,
		"BankAccountCZ": c.config.BankAccountCZ,
		"Content":       content,
		"Labels":        labels,
	}

	if qr := c.generateEmailQR(user, balance, minQRAmount); qr != "" {
		data["PaymentQRCode"] = qr
		data["QRAmount"] = math.Max(math.Abs(balance), minQRAmount)
	}

	return c.QueueEmail(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      content["subject"],
		TemplateName: "negative_balance.html",
		Data:         data,
	})
}

// SendDebtWarning sends warning about significant debt (>2x monthly fee).
// QR amount is max(abs(balance), monthlyFee) — never less than one month's fee.
func (c *Client) SendDebtWarning(ctx context.Context, user *db.User, balance float64, monthlyFee float64) error {
	lang := userLang(user)
	content := c.LoadContentBlocks(ctx, "debt_warning", lang)
	labels := EmailLabels(lang)

	data := map[string]interface{}{
		"Name":          displayName(user),
		"Balance":       balance,
		"AbsBalance":    math.Abs(balance),
		"MonthlyFee":    monthlyFee,
		"PaymentsID":    user.PaymentsID.String,
		"PortalURL":     c.config.BaseURL,
		"BankAccountCZ": c.config.BankAccountCZ,
		"Content":       content,
		"Labels":        labels,
	}

	if qr := c.generateEmailQR(user, balance, monthlyFee); qr != "" {
		data["PaymentQRCode"] = qr
		data["QRAmount"] = math.Max(math.Abs(balance), monthlyFee)
	}

	return c.QueueEmail(ctx, SendParams{
		UserID:       sql.NullInt64{Int64: user.ID, Valid: true},
		Recipient:    user.Email,
		Subject:      content["subject"],
		TemplateName: "debt_warning.html",
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
