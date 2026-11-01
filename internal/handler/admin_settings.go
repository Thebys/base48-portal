package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/base48/member-portal/internal/bank"
	"github.com/base48/member-portal/internal/db"
)

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
		"Title":                  "Nastavení",
		"User":                   user,
		"DBUser":                 dbUser,
		"CurrentBannerText":      bannerText,
		"CurrentBannerEnabled":   bannerEnabled,
		"CurrentBannerColor":     bannerColor,
		"CurrentAwaitingMessage": awaitingMessage,
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
