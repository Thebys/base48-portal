package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

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

	data := map[string]interface{}{
		"Title":          "Nastavení",
		"User":           user,
		"DBUser":         dbUser,
		"SMTPConfigured": smtpConfigured,
		"SMTPReachable":  smtpReachable,
		"SMTPSkipTLS":    h.config.SMTPSkipTLS,
		"EmailEnabled":   h.config.EmailEnabled,
		"BankAccountCZ":  h.config.BankAccountCZ,
		"RecentEmails":   emailViews,
		"OutboxCounts":   countsMap,
		"SentToday":      sentToday,
		"CurrentBannerText":       bannerText,
		"CurrentBannerEnabled":    bannerEnabled,
		"CurrentBannerColor":      bannerColor,
		"CurrentAwaitingMessage":  awaitingMessage,
	}

	h.render(w, "admin_settings.html", data)
}

// AdminTestEmailHandler sends test email (uses SendTemplated — bypasses outbox and EMAIL_ENABLED)
func (h *Handler) AdminTestEmailHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r) // auth enforced by RequireAdmin middleware

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	emailType := r.FormValue("type")
	recipient := r.FormValue("email")
	lang := r.FormValue("lang")
	if lang == "" {
		lang = "cs"
	}

	if recipient == "" {
		http.Error(w, "Email address is required", http.StatusBadRequest)
		return
	}

	// Get user by email for template data (use admin user as fallback)
	testUser, err := h.queries.GetUserByEmail(ctx, recipient)
	if err != nil {
		dbUser, err := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
			String: user.ID,
			Valid:  true,
		})
		if err != nil {
			http.Error(w, "Failed to get user data", http.StatusInternalServerError)
			return
		}
		testUser = dbUser
	}

	// Use nickname (username) for greeting, fall back to realname, then email
	testName := testUser.Username.String
	if testName == "" {
		testName = testUser.Realname.String
	}
	if testName == "" {
		testName = testUser.Email
	}

	templateName, data, err := h.buildEmailTestData(ctx, emailType, lang, testName, testUser.Username.String, testUser.PaymentsID.String)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	subject := data["Content"].(map[string]string)["subject"]

	// Use SendTemplated (direct SMTP, bypasses outbox + EMAIL_ENABLED)
	sendErr := h.emailClient.SendTemplated(ctx, email.SendParams{
		UserID:       sql.NullInt64{Int64: testUser.ID, Valid: true},
		Recipient:    recipient,
		Subject:      subject,
		TemplateName: templateName,
		Data:         data,
	})

	if sendErr != nil {
		// best-effort audit log
		h.queries.CreateLog(ctx, db.CreateLogParams{
			Subsystem: "email",
			Level:     "error",
			UserID:    sql.NullInt64{Int64: testUser.ID, Valid: true},
			Message:   "Test email failed: " + sendErr.Error(),
		})
		http.Error(w, "Failed to send test email: "+sendErr.Error(), http.StatusInternalServerError)
		return
	}

	// best-effort audit log
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "email",
		Level:     "success",
		UserID:    sql.NullInt64{Int64: testUser.ID, Valid: true},
		Message:   "Test email sent: " + emailType + " (" + lang + ") to " + recipient,
	})

	h.jsonSuccess(w, "Email odeslán na "+recipient)
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

	current := h.emailClient.LoadContentBlocks(ctx, templateName, lang)

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
	ctx := r.Context()

	var req struct {
		TemplateName string            `json:"template_name"`
		Lang         string            `json:"lang"`
		Blocks       map[string]string `json:"blocks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Lang == "" {
		req.Lang = "cs"
	}

	defaults := email.GetDefaultContentBlocks(req.Lang)
	if _, ok := defaults[req.TemplateName]; !ok {
		h.jsonError(w, "unknown template: "+req.TemplateName, http.StatusBadRequest)
		return
	}

	adminEmail := ""
	if user != nil {
		adminEmail = user.Email
	}

	for blockName, content := range req.Blocks {
		_, err := h.queries.UpsertEmailTemplateContent(ctx, db.UpsertEmailTemplateContentParams{
			TemplateName: req.TemplateName,
			BlockName:    blockName,
			Lang:         req.Lang,
			Content:      content,
			UpdatedBy:    sql.NullString{String: adminEmail, Valid: adminEmail != ""},
		})
		if err != nil {
			h.jsonError(w, fmt.Sprintf("Failed to save block %s: %v", blockName, err), http.StatusInternalServerError)
			return
		}
	}

	h.jsonSuccess(w, "Šablona uložena")
}

// AdminRetryEmailHandler retries a failed outbox email.
// POST /api/admin/email/retry
func (h *Handler) AdminRetryEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		idStr := r.FormValue("id")
		if idStr == "" {
			h.jsonError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		var err2 error
		req.ID, err2 = strconv.ParseInt(idStr, 10, 64)
		if err2 != nil {
			h.jsonError(w, "Invalid ID", http.StatusBadRequest)
			return
		}
	}

	if err := h.emailClient.RetryEmail(ctx, req.ID); err != nil {
		h.jsonError(w, "Retry failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, "Email znovu odeslán")
}

// AdminCancelEmailHandler cancels a pending outbox email.
// POST /api/admin/email/cancel
func (h *Handler) AdminCancelEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		idStr := r.FormValue("id")
		if idStr == "" {
			h.jsonError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		var err2 error
		req.ID, err2 = strconv.ParseInt(idStr, 10, 64)
		if err2 != nil {
			h.jsonError(w, "Invalid ID", http.StatusBadRequest)
			return
		}
	}

	_, err := h.queries.CancelEmailOutbox(ctx, req.ID)
	if err != nil {
		h.jsonError(w, "Zrušení selhalo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, "Email zrušen")
}

// AdminSendNowEmailHandler forces immediate delivery of a pending outbox email.
// POST /api/admin/email/send-now
func (h *Handler) AdminSendNowEmailHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		idStr := r.FormValue("id")
		if idStr == "" {
			h.jsonError(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		var err2 error
		req.ID, err2 = strconv.ParseInt(idStr, 10, 64)
		if err2 != nil {
			h.jsonError(w, "Invalid ID", http.StatusBadRequest)
			return
		}
	}

	if err := h.emailClient.SendNow(ctx, req.ID); err != nil {
		h.jsonError(w, "Odeslání selhalo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.jsonSuccess(w, "Email odeslán")
}

// buildEmailTestData builds template data and resolves the template filename
// for a given email type. Used by both test-send and preview handlers.
// paymentsID is the VS used for QR codes (real user's VS for test, "12345" for preview).
func (h *Handler) buildEmailTestData(ctx context.Context, emailType, lang, name, username, paymentsID string) (templateFile string, data map[string]interface{}, err error) {
	content := h.emailClient.LoadContentBlocks(ctx, emailType, lang)
	labels := email.EmailLabels(lang)

	data = map[string]interface{}{
		"Name":          name,
		"Username":      username,
		"PortalURL":     h.config.BaseURL,
		"BankAccountCZ": h.config.BankAccountCZ,
		"Content":       content,
		"Labels":        labels,
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
