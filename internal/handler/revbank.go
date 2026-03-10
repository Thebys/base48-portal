package handler

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/base48/member-portal/internal/db"
)

// --- API key middleware ---

// RequireRevbankAPIKey validates Bearer token against REVBANK_API_KEY.
func (h *Handler) RequireRevbankAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.config.RevbankAPIKey == "" {
			http.Error(w, `{"error":"revbank API key not configured"}`, http.StatusServiceUnavailable)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, `{"error":"missing or invalid Authorization header"}`, http.StatusUnauthorized)
			return
		}

		provided := strings.TrimPrefix(authHeader, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(h.config.RevbankAPIKey)) != 1 {
			http.Error(w, `{"error":"invalid API key"}`, http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// --- Sync types ---

type revbankSyncRequest struct {
	Accounts     []revbankAccountInput     `json:"accounts"`
	Transactions []revbankTransactionInput `json:"transactions"`
}

type revbankAccountInput struct {
	Username          string `json:"username"`
	BalanceCents      int64  `json:"balance_cents"`
	LastTransactionAt string `json:"last_transaction_at,omitempty"`
}

type revbankTransactionInput struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	AmountCents    int64  `json:"amount_cents"`
	Description    string `json:"description"`
	CounterAccount string `json:"counter_account,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type revbankSyncResponse struct {
	AccountsSynced     int      `json:"accounts_synced"`
	TransactionsSynced int      `json:"transactions_synced"`
	UsersMatched       int      `json:"users_matched"`
	UsersUnmatched     []string `json:"users_unmatched,omitempty"`
}

// --- Sync endpoint ---

// RevbankSyncHandler handles POST /api/revbank/sync
func (h *Handler) RevbankSyncHandler(w http.ResponseWriter, r *http.Request) {
	// Limit body size (10MB)
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

	var req revbankSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Build username -> user_id map (one query for all users)
	allUsers, err := h.queries.ListUsers(ctx)
	if err != nil {
		log.Printf("[RevBank] Failed to list users: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	usernameMap := make(map[string]int64, len(allUsers))
	for _, u := range allUsers {
		if u.Username.Valid && u.Username.String != "" {
			usernameMap[strings.ToLower(u.Username.String)] = u.ID
		}
	}

	var resp revbankSyncResponse
	matchedSet := make(map[string]bool)

	// Sync accounts
	for _, acct := range req.Accounts {
		if !isValidRevbankUsername(acct.Username) {
			continue
		}

		var userID sql.NullInt64
		if id, ok := usernameMap[strings.ToLower(acct.Username)]; ok {
			userID = sql.NullInt64{Int64: id, Valid: true}
			matchedSet[acct.Username] = true
		} else {
			resp.UsersUnmatched = append(resp.UsersUnmatched, acct.Username)
		}

		var lastTx sql.NullTime
		if acct.LastTransactionAt != "" {
			if t, err := parseFlexibleTime(acct.LastTransactionAt); err == nil {
				lastTx = sql.NullTime{Time: t, Valid: true}
			}
		}

		_, err := h.queries.UpsertRevbankAccount(ctx, db.UpsertRevbankAccountParams{
			Username:          acct.Username,
			UserID:            userID,
			BalanceCents:      acct.BalanceCents,
			LastTransactionAt: lastTx,
		})
		if err != nil {
			log.Printf("[RevBank] Failed to upsert account %s: %v", acct.Username, err)
			continue
		}
		resp.AccountsSynced++
	}

	// Sync transactions
	for _, tx := range req.Transactions {
		if tx.ID == "" || tx.Username == "" || len(tx.ID) > 200 || len(tx.Description) > 500 {
			continue
		}
		if !isValidRevbankUsername(tx.Username) {
			continue
		}
		if math.Abs(float64(tx.AmountCents)) > 10_000_000 {
			continue
		}

		var userID sql.NullInt64
		if id, ok := usernameMap[strings.ToLower(tx.Username)]; ok {
			userID = sql.NullInt64{Int64: id, Valid: true}
		}

		createdAt, err := parseFlexibleTime(tx.CreatedAt)
		if err != nil {
			continue
		}

		err = h.queries.UpsertRevbankTransaction(ctx, db.UpsertRevbankTransactionParams{
			TransactionID:  tx.ID,
			Username:       tx.Username,
			UserID:         userID,
			AmountCents:    tx.AmountCents,
			Description:    tx.Description,
			CounterAccount: sql.NullString{String: tx.CounterAccount, Valid: tx.CounterAccount != ""},
			CreatedAt:      createdAt,
		})
		if err != nil {
			log.Printf("[RevBank] Failed to upsert transaction %s: %v", tx.ID, err)
			continue
		}
		resp.TransactionsSynced++
	}

	resp.UsersMatched = len(matchedSet)

	// Update last sync timestamp
	h.queries.UpsertSetting(ctx, db.UpsertSettingParams{
		Key:   "revbank_last_sync",
		Value: time.Now().Format(time.RFC3339),
	})

	// Log
	h.queries.CreateLog(ctx, db.CreateLogParams{
		Subsystem: "revbank",
		Level:     "info",
		Message: fmt.Sprintf("Sync: %d accounts, %d transactions, %d matched",
			resp.AccountsSynced, resp.TransactionsSynced, resp.UsersMatched),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- Admin page ---

// AdminRevbankHandler renders the admin RevBank overview page.
func (h *Handler) AdminRevbankHandler(w http.ResponseWriter, r *http.Request) {
	user := h.auth.GetUser(r)
	ctx := r.Context()

	adminDBUser, _ := h.queries.GetUserByKeycloakID(ctx, sql.NullString{
		String: user.ID,
		Valid:  true,
	})

	accounts, _ := h.queries.ListRevbankAccounts(ctx)
	transactions, _ := h.queries.ListRevbankRecentTransactions(ctx, 50)

	var lastSync string
	if setting, err := h.queries.GetSetting(ctx, "revbank_last_sync"); err == nil {
		lastSync = setting.Value
	}

	data := map[string]interface{}{
		"Title":        "RevBank",
		"User":         user,
		"DBUser":       adminDBUser,
		"Accounts":     accounts,
		"Transactions": transactions,
		"LastSync":     lastSync,
	}

	h.render(w, "admin_revbank.html", data)
}

// --- Helpers ---

// isValidRevbankUsername returns true for normal user accounts (not hidden/system).
func isValidRevbankUsername(name string) bool {
	if name == "" || len(name) > 100 {
		return false
	}
	// Skip system accounts
	if name[0] == '+' || name[0] == '-' || name[0] == '*' {
		return false
	}
	// Reject control characters
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// parseFlexibleTime parses RFC3339 or RevBank's YYYY-MM-DD_HH:MM:SS format.
func parseFlexibleTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// RevBank format: 2026-03-10_22:36:06
	return time.Parse("2006-01-02_15:04:05", s)
}

// formatCentsAsCZK formats cents as "25,00" or "-10,50".
func formatCentsAsCZK(cents int64) string {
	negative := cents < 0
	if negative {
		cents = -cents
	}
	whole := cents / 100
	frac := cents % 100
	s := fmt.Sprintf("%s,%02d", formatNumber(whole), frac)
	if negative {
		return "-" + s
	}
	return s
}
