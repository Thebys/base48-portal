package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/db"
	"github.com/base48/member-portal/internal/email"
)

// EmailOutboxView wraps an outbox entry with computed display fields.
type EmailOutboxView struct {
	db.EmailOutbox
	TimeRemaining string // "47h 23m" or "" if not scheduled
}

// AdminSettingsHandler shows admin settings page
func (h *Handler) AdminSettingsHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware
	ctx := r.Context()

	dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})
	if err != nil {
		log.Printf("[Handler] failed to fetch admin DB user: %v", err)
	}

	// Check SMTP: not just configured, but actually reachable
	smtpConfigured := h.config.SMTPHost != "" && h.config.SMTPPort != 0
	smtpReachable := false
	if smtpConfigured {
		addr := net.JoinHostPort(h.config.SMTPHost, strconv.Itoa(h.config.SMTPPort))
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			conn.Close()
			smtpReachable = true
		}
	}

	// Outbox data
	recentEmails, err := h.queries.ListRecentEmailOutbox(ctx, 80)
	if err != nil {
		log.Printf("[Handler] failed to fetch recent email outbox: %v", err)
	}
	outboxCounts, err := h.queries.CountEmailOutboxByStatus(ctx)
	if err != nil {
		log.Printf("[Handler] failed to count email outbox by status: %v", err)
	}
	sentToday, err := h.queries.CountEmailOutboxSentToday(ctx)
	if err != nil {
		log.Printf("[Handler] failed to count emails sent today: %v", err)
	}

	countsMap := map[string]int64{"pending": 0, "sent": 0, "failed": 0, "cancelled": 0}
	for _, c := range outboxCounts {
		countsMap[c.Status] = c.Count
	}

	// Enrich outbox entries with countdown
	now := time.Now()
	var emailViews []EmailOutboxView
	for _, e := range recentEmails {
		view := EmailOutboxView{EmailOutbox: e}
		if e.Status == "pending" && e.NextRetryAt.Valid && e.NextRetryAt.Time.After(now) {
			remaining := e.NextRetryAt.Time.Sub(now)
			hours := int(remaining.Hours())
			minutes := int(remaining.Minutes()) % 60
			if hours > 0 {
				view.TimeRemaining = fmt.Sprintf("%dh %dm", hours, minutes)
			} else {
				view.TimeRemaining = fmt.Sprintf("%dm", minutes)
			}
		}
		emailViews = append(emailViews, view)
	}

	// Load awaiting member message
	awaitingMessage := ""
	if s, err := h.queries.GetSetting(ctx, "awaiting_message"); err == nil {
		awaitingMessage = s.Value
	}

	// Load emergency banner settings
	bannerText := ""
	bannerEnabled := false
	bannerColor := "#dc2626"
	if b, err := h.queries.GetSetting(ctx, "banner_text"); err == nil {
		bannerText = b.Value
	}
	if b, err := h.queries.GetSetting(ctx, "banner_enabled"); err == nil && b.Value == "true" {
		bannerEnabled = true
	}
	if b, err := h.queries.GetSetting(ctx, "banner_color"); err == nil && b.Value != "" {
		bannerColor = b.Value
	}

	// Bar debt reminder settings, plus how many members they would hit today
	barDebt := email.LoadBarDebtSettings(ctx, h.queries)
	barDebtors, err := h.queries.ListAcceptedBarDebtors(ctx, barDebt.MaxBalanceCents())
	if err != nil {
		log.Printf("[Handler] failed to list bar debtors: %v", err)
	}

	data := map[string]interface{}{
		"Title":                  "Nastavení",
		"User":                   user,
		"DBUser":                 dbUser,
		"SMTPConfigured":         smtpConfigured,
		"SMTPReachable":          smtpReachable,
		"SMTPSkipTLS":            h.config.SMTPSkipTLS,
		"EmailEnabled":           h.config.EmailEnabled,
		"RecentEmails":           emailViews,
		"OutboxCounts":           countsMap,
		"SentToday":              sentToday,
		"CurrentBannerText":      bannerText,
		"CurrentBannerEnabled":   bannerEnabled,
		"CurrentBannerColor":     bannerColor,
		"CurrentAwaitingMessage": awaitingMessage,
		"BarDebt":                barDebt,
		"BarDebtorsNow":          len(barDebtors),
	}
	h.addPaymentAccountView(ctx, data, time.Now())

	h.render(w, "admin_settings.html", data)
}

// addPaymentAccountView adds what the settings page shows about the account
// members pay into: the account the FIO sync last recorded and how fresh that is.
func (h *Handler) addPaymentAccountView(ctx context.Context, data map[string]interface{}, now time.Time) {
	acc, known := bank.Load(ctx, h.queries)
	data["FIOConfigured"] = h.config.BankFIOToken != ""
	data["PaymentAccount"] = acc
	data["PaymentAccountKnown"] = known
	data["PaymentAccountStale"] = known && acc.Stale(now)
	data["PaymentAccountCheckedAt"] = acc.CheckedAt.Local().Format("2. 1. 2006 15:04")
}

// AdminTestEmailHandler sends a test email to the admin who asks for it, and
// only to them: rendered with the admin's own name and VS, it goes straight to
// SMTP, outside the outbox and regardless of EMAIL_ENABLED, and says [TEST] in
// the subject. An address typed into the form used to be honoured, so one
// click could mail a real member a made-up debt notice.
func (h *Handler) AdminTestEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formulář nejde přečíst", http.StatusBadRequest)
		return
	}
	emailType := r.FormValue("type")
	lang := r.FormValue("lang")
	if lang != "en" {
		lang = "cs"
	}

	admin, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{String: user.ID, Valid: true})
	if err != nil {
		http.Error(w, "Tvůj účet v portálu nejde načíst", http.StatusInternalServerError)
		return
	}

	templateName, data, err := h.buildEmailTestData(ctx, emailType, lang, admin.DisplayName(), admin.Username.String, admin.PaymentsID.String)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	subject := "[TEST] " + data["Content"].(map[string]string)["subject"]

	sendErr := h.emailClient.SendTemplated(ctx, email.SendParams{
		UserID:       sql.NullInt64{Int64: admin.ID, Valid: true},
		Recipient:    admin.Email,
		Subject:      subject,
		TemplateName: templateName,
		Data:         data,
	})

	level, message := "success", fmt.Sprintf("Testovací e-mail %s (%s) odeslán na %s", emailType, lang, admin.Email)
	if sendErr != nil {
		level, message = "error", fmt.Sprintf("Testovací e-mail %s (%s) na %s se nepodařilo odeslat: %v", emailType, lang, admin.Email, sendErr)
	}
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     level,
		UserID:    sql.NullInt64{Int64: admin.ID, Valid: true},
		Message:   message,
	})
	if sendErr != nil {
		http.Error(w, "Testovací e-mail se nepodařilo odeslat: "+sendErr.Error(), http.StatusInternalServerError)
		return
	}
	h.jsonSuccess(w, "Testovací e-mail odeslán na "+admin.Email)
}

// AdminGetTemplateContentHandler returns editable content blocks for a template.
// GET /api/admin/email/templates?template=welcome&lang=cs
func (h *Handler) AdminGetTemplateContentHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	templateName := r.URL.Query().Get("template")
	if templateName == "" {
		h.jsonError(w, "template parameter required", http.StatusBadRequest)
		return
	}
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "cs"
	}

	defaults := email.GetDefaultContentBlocks(lang)
	templateDefaults, ok := defaults[templateName]
	if !ok {
		h.jsonError(w, "unknown template: "+templateName, http.StatusBadRequest)
		return
	}

	current := h.emailClient.LoadRawContentBlocks(ctx, templateName, lang)

	type blockInfo struct {
		Name       string `json:"name"`
		Default    string `json:"default"`
		Current    string `json:"current"`
		IsModified bool   `json:"is_modified"`
	}

	blocks := make([]blockInfo, 0, len(templateDefaults))
	for name, def := range templateDefaults {
		cur := current[name]
		blocks = append(blocks, blockInfo{
			Name:       name,
			Default:    def,
			Current:    cur,
			IsModified: cur != def,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"template": templateName,
		"lang":     lang,
		"blocks":   blocks,
	})
}

// AdminSaveTemplateContentHandler saves content block overrides.
// POST /api/admin/email/templates
func (h *Handler) AdminSaveTemplateContentHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)

	var req struct {
		TemplateName string            `json:"template_name"`
		Lang         string            `json:"lang"`
		Blocks       map[string]string `json:"blocks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	adminEmail := ""
	if user != nil {
		adminEmail = user.Email
	}

	if err := h.saveTemplateBlocks(r.Context(), req.TemplateName, req.Lang, req.Blocks, adminEmail); err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.jsonSuccess(w, "Šablona uložena")
}

// saveTemplateBlocks stores the editor's blocks as overrides. A block equal to
// the default is stored as no override at all (and any old one removed), so a
// later change to the default in code still reaches it.
func (h *Handler) saveTemplateBlocks(ctx context.Context, templateName, lang string, blocks map[string]string, adminEmail string) error {
	if lang == "" {
		lang = "cs"
	}
	templateDefaults, ok := email.GetDefaultContentBlocks(lang)[templateName]
	if !ok {
		return fmt.Errorf("unknown template: %s", templateName)
	}
	for blockName := range blocks {
		if _, ok := templateDefaults[blockName]; !ok {
			return fmt.Errorf("unknown block: %s", blockName)
		}
	}

	for blockName, content := range blocks {
		content = email.NormalizeBlockText(content)

		if content == templateDefaults[blockName] {
			err := h.queries.DeleteEmailTemplateContent(ctx, db.DeleteEmailTemplateContentParams{
				TemplateName: templateName,
				BlockName:    blockName,
				Lang:         lang,
			})
			if err != nil {
				return fmt.Errorf("failed to reset block %s: %w", blockName, err)
			}
			continue
		}

		_, err := h.queries.UpsertEmailTemplateContent(ctx, db.UpsertEmailTemplateContentParams{
			TemplateName: templateName,
			BlockName:    blockName,
			Lang:         lang,
			Content:      content,
			UpdatedBy:    sql.NullString{String: adminEmail, Valid: adminEmail != ""},
		})
		if err != nil {
			return fmt.Errorf("failed to save block %s: %w", blockName, err)
		}
	}
	return nil
}

// outboxID reads the outbox entry an admin acts on, from a JSON body
// {"id": N} or a form field id.
func outboxID(r *http.Request) (int64, error) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.ID > 0 {
		return req.ID, nil
	}
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("chybí číslo e-mailu")
	}
	return id, nil
}

// adminEmail is the signed-in admin, for audit lines and test emails.
// RequireAdmin has let them in.
func (h *Handler) adminEmail(r *http.Request) string {
	if admin := h.auth.GetUser(r); admin != nil {
		return admin.Email
	}
	return ""
}

// auditOutbox records an admin's action on an outbox entry.
func (h *Handler) auditOutbox(r *http.Request, id int64, action string) {
	h.queries.CreateLog(context.WithoutCancel(r.Context()), db.CreateLogParams{
		Subsystem: "email",
		Level:     "info",
		Message:   fmt.Sprintf("E-mail #%d: %s (%s)", id, action, h.adminEmail(r)),
	})
}

// outboxActionError answers a send-now or retry that did not go out.
func (h *Handler) outboxActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, email.ErrBusy):
		h.jsonError(w, "E-mail se právě odesílá, zkus to za pár minut", http.StatusConflict)
	case errors.Is(err, email.ErrEmailDisabled):
		h.jsonError(w, "E-maily jsou vypnuté (EMAIL_ENABLED=false)", http.StatusConflict)
	default:
		h.jsonError(w, "Neodesláno: "+err.Error(), http.StatusUnprocessableEntity)
	}
}

// AdminRetryEmailHandler sends a failed outbox email again.
// POST /api/admin/email/retry
func (h *Handler) AdminRetryEmailHandler(w http.ResponseWriter, r *http.Request) {
	id, err := outboxID(r)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	sendErr := h.emailClient.RetryEmail(r.Context(), id)
	h.auditOutbox(r, id, "znovu odeslat: "+outcomeText(sendErr))
	if sendErr != nil {
		h.outboxActionError(w, sendErr)
		return
	}
	h.jsonSuccess(w, "E-mail odeslán")
}

// AdminCancelEmailHandler cancels a pending outbox email.
// POST /api/admin/email/cancel
func (h *Handler) AdminCancelEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := outboxID(r)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, err = h.queries.CancelEmailOutbox(ctx, db.CancelEmailOutboxParams{
		Reason: sql.NullString{String: "Zrušil(a) " + h.adminEmail(r), Valid: true},
		ID:     id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		h.jsonError(w, "E-mail už nečeká na odeslání, nebo se právě odesílá", http.StatusConflict)
		return
	}
	if err != nil {
		h.jsonError(w, "Zrušení selhalo: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.auditOutbox(r, id, "zrušen")
	h.jsonSuccess(w, "E-mail zrušen")
}

// AdminSendNowEmailHandler delivers a pending outbox email without waiting
// for its time, after the same last look as the worker gives it.
// POST /api/admin/email/send-now
func (h *Handler) AdminSendNowEmailHandler(w http.ResponseWriter, r *http.Request) {
	id, err := outboxID(r)
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	sendErr := h.emailClient.SendNow(r.Context(), id)
	h.auditOutbox(r, id, "odeslat hned: "+outcomeText(sendErr))
	if sendErr != nil {
		h.outboxActionError(w, sendErr)
		return
	}
	h.jsonSuccess(w, "E-mail odeslán")
}

func outcomeText(err error) string {
	if err != nil {
		return err.Error()
	}
	return "odesláno"
}

// buildEmailTestData builds template data and resolves the template filename
// for a given email type. Used by both test-send and preview handlers.
// paymentsID is the VS used for QR codes (real user's VS for test, "12345" for preview).
func (h *Handler) buildEmailTestData(ctx context.Context, emailType, lang, name, username, paymentsID string) (templateFile string, data map[string]interface{}, err error) {
	content := h.emailClient.LoadContentBlocks(ctx, emailType, lang)
	labels := email.EmailLabels(lang)

	data = map[string]interface{}{
		"Name":      name,
		"Username":  username,
		"PortalURL": h.config.BaseURL,
		"Content":   content,
		"Labels":    labels,
	}

	// Debt reminders carry payment details. Like the real ones, they cannot be
	// rendered before the FIO sync has recorded the account.
	if emailType == "negative_balance" || emailType == "debt_warning" {
		acc, ok := bank.Load(ctx, h.queries)
		if !ok {
			return "", nil, bank.ErrUnknown
		}
		email.AddPaymentAccount(data, acc)
	}

	switch emailType {
	case "registration":
		templateFile = "registration.html"
	case "welcome":
		templateFile = "welcome.html"
	case "negative_balance":
		templateFile = "negative_balance.html"
		data["Balance"] = -500.0
		data["AbsBalance"] = 500.0
		data["PaymentsID"] = paymentsID
		data["QRAmount"] = 1000.0
		if qr := h.emailClient.GenerateQRForEmail(paymentsID, 1000); qr != "" {
			data["PaymentQRCode"] = qr
		}
	case "debt_warning":
		templateFile = "debt_warning.html"
		data["Balance"] = -2400.0
		data["AbsBalance"] = 2400.0
		data["MonthlyFee"] = 1000.0
		data["PaymentsID"] = paymentsID
		data["QRAmount"] = 2400.0
		if qr := h.emailClient.GenerateQRForEmail(paymentsID, 2400); qr != "" {
			data["PaymentQRCode"] = qr
		}
	case "membership_suspended":
		templateFile = "membership_suspended.html"
		data["Reason"] = "Dluh na členském příspěvku přesahuje povolený limit."
	case email.TemplateBarDebt:
		templateFile = email.TemplateFileBarDebt
		threshold := email.LoadBarDebtSettings(ctx, h.queries).ThresholdCZK
		data["BalanceCents"] = int64(-35050)
		data["BalanceText"] = email.FormatCZKCents(-35050)
		data["ThresholdText"] = strconv.FormatInt(threshold, 10)
	default:
		return "", nil, fmt.Errorf("unknown template: %s", emailType)
	}

	return templateFile, data, nil
}

// AdminPreviewEmailHandler renders a template with test data for preview.
// POST /api/admin/email/preview
func (h *Handler) AdminPreviewEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		TemplateName string `json:"template_name"`
		Lang         string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Lang == "" {
		req.Lang = "cs"
	}

	templateFile, data, err := h.buildEmailTestData(ctx, req.TemplateName, req.Lang, "Jan Novák", "jan.novak", "12345")
	if err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	rendered, err := h.emailClient.RenderPreview(email.SendParams{
		TemplateName: templateFile,
		Data:         data,
	})
	if err != nil {
		h.jsonError(w, "Render failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"html":    rendered,
	})
}

// AdminSaveAwaitingMessageHandler saves the awaiting member message.
// POST /api/admin/awaiting-message
func (h *Handler) AdminSaveAwaitingMessageHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if _, err := h.queries.UpsertSetting(ctx, db.UpsertSettingParams{Key: "awaiting_message", Value: req.Message}); err != nil {
		h.jsonError(w, "Failed to save awaiting message: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, "Zpráva uložena")
}

// AdminSaveBannerHandler saves the emergency banner settings.
// POST /api/admin/banner
func (h *Handler) AdminSaveBannerHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Text    string `json:"text"`
		Enabled bool   `json:"enabled"`
		Color   string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	enabledStr := "false"
	if req.Enabled {
		enabledStr = "true"
	}

	if _, err := h.queries.UpsertSetting(ctx, db.UpsertSettingParams{Key: "banner_text", Value: req.Text}); err != nil {
		h.jsonError(w, "Failed to save banner text: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := h.queries.UpsertSetting(ctx, db.UpsertSettingParams{Key: "banner_enabled", Value: enabledStr}); err != nil {
		h.jsonError(w, "Failed to save banner state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Color != "" {
		if _, err := h.queries.UpsertSetting(ctx, db.UpsertSettingParams{Key: "banner_color", Value: req.Color}); err != nil {
			h.jsonError(w, "Failed to save banner color: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	h.jsonSuccess(w, "Banner uložen")
}

// AdminSaveBarDebtSettingsHandler saves the bar debt reminder settings.
// POST /api/admin/bar-debt-settings
func (h *Handler) AdminSaveBarDebtSettingsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		Enabled      bool  `json:"enabled"`
		ThresholdCZK int64 `json:"threshold_czk"`
		IntervalDays int   `json:"interval_days"`
		DelayHours   int   `json:"delay_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	settings := email.BarDebtSettings{
		Enabled:      req.Enabled,
		ThresholdCZK: req.ThresholdCZK,
		IntervalDays: req.IntervalDays,
		DelayHours:   req.DelayHours,
	}
	if err := settings.Validate(); err != nil {
		h.jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := email.SaveBarDebtSettings(ctx, h.queries, settings); err != nil {
		h.jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	adminEmail := ""
	if admin := h.auth.GetUser(r); admin != nil {
		adminEmail = admin.Email
	}
	state := "disabled"
	if settings.Enabled {
		state = "enabled"
	}
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "info",
		Message: fmt.Sprintf("Bar debt reminders %s: debt above %d Kč, every %d days, delay %dh (by %s)",
			state, settings.ThresholdCZK, settings.IntervalDays, settings.DelayHours, adminEmail),
	})

	debtors, err := h.queries.ListAcceptedBarDebtors(ctx, settings.MaxBalanceCents())
	if err != nil {
		log.Printf("[Handler] failed to list bar debtors: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Nastavení uloženo",
		"debtors": len(debtors),
	})
}
