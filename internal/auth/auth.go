package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"

	"github.com/base48/member-portal/internal/config"
	"github.com/base48/member-portal/internal/db"
)

const (
	sessionName     = "base48-session"
	sessionUserKey  = "user"
	sessionStateKey = "oauth_state"
)

// User represents the authenticated user from Keycloak
type User struct {
	ID            string   `json:"sub"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Name          string   `json:"name"`
	PreferredName string   `json:"preferred_username"`
	Roles         []string `json:"roles"`
}

// Authenticator handles Keycloak OIDC authentication
type Authenticator struct {
	provider     *oidc.Provider
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
	store        *sessions.CookieStore
	config       *config.Config
	queries      *db.Queries
	disabled     bool // true if Keycloak is unavailable
}

func init() {
	// Register User type for session serialization
	gob.Register(&User{})
}

// New creates a new Authenticator instance
func New(ctx context.Context, cfg *config.Config, queries *db.Queries) (*Authenticator, error) {
	// Create HTTP client with aggressive timeouts for startup
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 3 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 3 * time.Second,
		},
	}

	// Try to connect to Keycloak with timeout
	providerCtx := context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	provider, err := oidc.NewProvider(providerCtx, cfg.KeycloakIssuerURL())
	if err != nil {
		// Keycloak unavailable - start in limited mode
		fmt.Printf("⚠ WARNING: Keycloak unavailable at %s\n", cfg.KeycloakIssuerURL())
		fmt.Printf("⚠ Error: %v\n", err)
		fmt.Println("⚠ Starting in LIMITED MODE - authentication will be unavailable")

		store := sessions.NewCookieStore([]byte(cfg.SessionSecret))
		store.Options = &sessions.Options{
			Path:     "/",
			MaxAge:   86400 * 7,
			HttpOnly: true,
			Secure:   false,
			SameSite: http.SameSiteLaxMode,
		}

		return &Authenticator{
			store:    store,
			config:   cfg,
			queries:  queries,
			disabled: true,
		}, nil
	}

	oauth2Config := oauth2.Config{
		ClientID:     cfg.KeycloakClientID,
		ClientSecret: cfg.KeycloakClientSecret,
		RedirectURL:  cfg.OAuthCallbackURL(),
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: cfg.KeycloakClientID,
	})

	store := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		Secure:   len(cfg.BaseURL) >= 5 && cfg.BaseURL[:5] == "https",
		SameSite: http.SameSiteLaxMode,
	}

	fmt.Println("✓ Keycloak connection established")

	return &Authenticator{
		provider:     provider,
		oauth2Config: oauth2Config,
		verifier:     verifier,
		store:        store,
		config:       cfg,
		queries:      queries,
		disabled:     false,
	}, nil
}

// LoginHandler redirects to Keycloak login
func (a *Authenticator) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if a.disabled {
		http.Error(w, "Authentication unavailable - Identity Provider (Keycloak) is not accessible", http.StatusServiceUnavailable)
		return
	}

	state := generateState()

	session, _ := a.store.Get(r, sessionName)
	session.Values[sessionStateKey] = state
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Failed to save session", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, a.oauth2Config.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

// CallbackHandler handles the OAuth2 callback from Keycloak
func (a *Authenticator) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if a.disabled {
		http.Error(w, "Authentication unavailable - Identity Provider (Keycloak) is not accessible", http.StatusServiceUnavailable)
		return
	}

	session, err := a.store.Get(r, sessionName)
	if err != nil {
		http.Error(w, "Failed to get session", http.StatusInternalServerError)
		return
	}

	// Verify state
	savedState, ok := session.Values[sessionStateKey].(string)
	if !ok || savedState != r.URL.Query().Get("state") {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}
	delete(session.Values, sessionStateKey)

	// Exchange code for token
	code := r.URL.Query().Get("code")
	token, err := a.oauth2Config.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}

	// Extract ID token
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "No ID token in response", http.StatusInternalServerError)
		return
	}

	// Verify ID token
	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "Failed to verify ID token", http.StatusInternalServerError)
		return
	}

	// Extract user info and roles
	var claims struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		PreferredName string `json:"preferred_username"`
		RealmAccess   struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
		ResourceAccess map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}

	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "Failed to parse claims", http.StatusInternalServerError)
		return
	}

	// Extract only member portal roles (whitelist approach)
	allowedRoles := map[string]bool{
		"memberportal_admin": true,
		"active_member":      true,
		"in_debt":            true,
	}

	roles := make([]string, 0)

	// Filter realm roles
	for _, role := range claims.RealmAccess.Roles {
		if allowedRoles[role] {
			roles = append(roles, role)
		}
	}

	// Add client-specific roles (from your Keycloak client)
	if clientRoles, ok := claims.ResourceAccess[a.config.KeycloakClientID]; ok {
		for _, role := range clientRoles.Roles {
			if allowedRoles[role] {
				roles = append(roles, role)
			}
		}
	}

	user := User{
		ID:            claims.Sub,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		Name:          claims.Name,
		PreferredName: claims.PreferredName,
		Roles:         roles,
	}

	// Store user in session (but NOT the full token - it's too big for cookies)
	// For admin operations, we'll use service account instead
	session.Values[sessionUserKey] = &user
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Failed to save session", http.StatusInternalServerError)
		return
	}

	// Log successful login
	if a.queries != nil {
		// Try to get user ID from database (may not exist yet for new users)
		dbUser, err := a.queries.GetUserByKeycloakID(r.Context(), sql.NullString{
			String: user.ID,
			Valid:  true,
		})

		var userID sql.NullInt64
		if err == nil {
			userID = sql.NullInt64{Int64: dbUser.ID, Valid: true}
		}

		// Log login (gracefully - don't fail login if logging fails)
		_, _ = a.queries.CreateLog(r.Context(), db.CreateLogParams{
			Subsystem: "auth",
			Level:     "info",
			UserID:    userID,
			Message:   fmt.Sprintf("User login: %s", user.Email),
			Metadata:  sql.NullString{String: fmt.Sprintf(`{"keycloak_id":"%s","email":"%s"}`, user.ID, user.Email), Valid: true},
		})
	}

	// Redirect to dashboard
	http.Redirect(w, r, "/dashboard", http.StatusTemporaryRedirect)
}

// LogoutHandler clears the session
func (a *Authenticator) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := a.store.Get(r, sessionName)
	session.Values = make(map[interface{}]interface{})
	session.Options.MaxAge = -1
	session.Save(r, w)

	// Redirect to Keycloak logout (optional)
	// For now, just redirect to home
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// GetUser returns the authenticated user from session, or nil if not authenticated
func (a *Authenticator) GetUser(r *http.Request) *User {
	session, err := a.store.Get(r, sessionName)
	if err != nil {
		return nil
	}

	user, ok := session.Values[sessionUserKey].(*User)
	if !ok {
		return nil
	}

	return user
}

// RequireAuth is a middleware that ensures the user is authenticated
func (a *Authenticator) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.GetUser(r)
		if user == nil {
			http.Redirect(w, r, "/auth/login", http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HasRole checks if user has a specific role
func (u *User) HasRole(role string) bool {
	if u == nil {
		return false
	}
	for _, r := range u.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// HasAnyRole checks if user has any of the specified roles
func (u *User) HasAnyRole(roles ...string) bool {
	if u == nil {
		return false
	}
	for _, role := range roles {
		if u.HasRole(role) {
			return true
		}
	}
	return false
}

// IsAdmin checks if user has memberportal_admin role
func (u *User) IsAdmin() bool {
	return u.HasRole("memberportal_admin")
}

// IsActiveMember checks if user has active_member role
func (u *User) IsActiveMember() bool {
	return u.HasRole("active_member")
}

// IsInDebt checks if user has in_debt role
func (u *User) IsInDebt() bool {
	return u.HasRole("in_debt")
}

// generateState creates a random state string for OAuth2
func generateState() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback (shouldn't happen)
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.URLEncoding.EncodeToString(b)
}
