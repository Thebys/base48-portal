package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
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

// KeycloakApplication represents a client/service visible in Keycloak
type KeycloakApplication struct {
	ClientID     string `json:"clientId"`
	ClientName   string `json:"clientName"`
	Description  string `json:"description"`
	EffectiveURL string `json:"effectiveUrl"`
	RootURL      string `json:"rootUrl"`
	BaseURL      string `json:"baseUrl"`
	LogoURI      string `json:"logoUri"`
	InUse        bool   `json:"inUse"`
}

// DisplayName returns ClientName if set, otherwise ClientID
func (a KeycloakApplication) DisplayName() string {
	if a.ClientName != "" {
		return a.ClientName
	}
	return a.ClientID
}

// URL returns the best available URL for the application
func (a KeycloakApplication) URL() string {
	if a.EffectiveURL != "" {
		return a.EffectiveURL
	}
	if a.RootURL != "" && a.BaseURL != "" {
		if joined, err := url.JoinPath(a.RootURL, a.BaseURL); err == nil {
			return joined
		}
	}
	if a.BaseURL != "" {
		return a.BaseURL
	}
	return a.RootURL
}

// User represents the authenticated user from Keycloak
type User struct {
	ID            string   `json:"sub"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Name          string   `json:"name"`
	GivenName     string   `json:"given_name"`
	FamilyName    string   `json:"family_name"`
	PreferredName string   `json:"preferred_username"`
	Roles         []string `json:"roles"`
	Locale        string   `json:"locale"`
}

// keycloakState bundles the OIDC primitives that are only valid once Keycloak
// has been successfully contacted. The whole struct is swapped atomically
// under Authenticator.mu, so handlers that grab a snapshot see a consistent
// triplet (provider/oauth2Config/verifier) regardless of reconnects.
type keycloakState struct {
	provider     *oidc.Provider
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
}

// Authenticator handles Keycloak OIDC authentication
type Authenticator struct {
	// Immutable after construction.
	store      *sessions.CookieStore
	config     *config.Config
	queries    *db.Queries
	httpClient *http.Client // IPv4-only client for Keycloak communication

	// Mutable Keycloak state. nil until the first successful connect; replaced
	// atomically by connect() so handlers never observe a half-initialised
	// triplet. Read via keycloak(), write only inside connect().
	mu sync.RWMutex
	kc *keycloakState

	appCache   map[string][]KeycloakApplication // keyed by Keycloak user ID
	appCacheMu sync.RWMutex
}

func init() {
	// Register User type for session serialization
	gob.Register(&User{})
}

// New creates a new Authenticator. It tries once to reach Keycloak so the
// happy-path startup returns a fully ready authenticator. If Keycloak is
// unreachable, the authenticator is returned in limited mode and a background
// goroutine keeps retrying until it succeeds (or ctx is cancelled), at which
// point the authenticator becomes fully functional without a process restart.
func New(ctx context.Context, cfg *config.Config, queries *db.Queries) (*Authenticator, error) {
	// Force IPv4 — Keycloak's IPv6 endpoint resets TLS connections in our
	// VPS environment.
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp4", addr)
			},
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 3 * time.Second,
		},
	}

	store := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   3600, // 60 minutes — forces re-login to refresh Keycloak roles
		HttpOnly: true,
		Secure:   strings.HasPrefix(cfg.BaseURL, "https"),
		SameSite: http.SameSiteLaxMode,
	}

	a := &Authenticator{
		store:      store,
		config:     cfg,
		queries:    queries,
		httpClient: httpClient,
		appCache:   make(map[string][]KeycloakApplication),
	}

	if err := a.connect(ctx); err != nil {
		fmt.Printf("⚠ WARNING: Keycloak unavailable at %s: %v\n", cfg.KeycloakIssuerURL(), err)
		fmt.Println("⚠ Starting in LIMITED MODE — will reconnect automatically once Keycloak is reachable")
		go a.reconnectLoop(ctx)
	} else {
		fmt.Println("✓ Keycloak connection established")
	}

	return a, nil
}

// connect performs OIDC discovery against Keycloak. On success it atomically
// publishes a fresh keycloakState so handlers (which read via keycloak())
// never see a half-built triplet.
func (a *Authenticator) connect(ctx context.Context) error {
	providerCtx := context.WithValue(ctx, oauth2.HTTPClient, a.httpClient)
	provider, err := oidc.NewProvider(providerCtx, a.config.KeycloakIssuerURL())
	if err != nil {
		return err
	}

	state := &keycloakState{
		provider: provider,
		oauth2Config: oauth2.Config{
			ClientID:     a.config.KeycloakClientID,
			ClientSecret: a.config.KeycloakClientSecret,
			RedirectURL:  a.config.OAuthCallbackURL(),
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{
			ClientID: a.config.KeycloakClientID,
		}),
	}

	a.mu.Lock()
	a.kc = state
	a.mu.Unlock()
	return nil
}

// reconnectLoop retries connect() with capped exponential backoff until it
// succeeds or ctx is cancelled. Started exactly once from New() when the
// initial connect fails; exits silently on success after logging recovery.
func (a *Authenticator) reconnectLoop(ctx context.Context) {
	backoff := 5 * time.Second
	const maxBackoff = 5 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if err := a.connect(ctx); err == nil {
			fmt.Println("✓ Keycloak connection established (recovered)")
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// keycloak returns the current ready Keycloak state, or nil if Keycloak has
// never been reached (or the authenticator is otherwise in limited mode).
// Callers MUST nil-check before dereferencing.
func (a *Authenticator) keycloak() *keycloakState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.kc
}

// LoginHandler redirects to Keycloak login
func (a *Authenticator) LoginHandler(w http.ResponseWriter, r *http.Request) {
	kc := a.keycloak()
	if kc == nil {
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

	http.Redirect(w, r, kc.oauth2Config.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

// CallbackHandler handles the OAuth2 callback from Keycloak
func (a *Authenticator) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	kc := a.keycloak()
	if kc == nil {
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

	// Exchange code for token (use IPv4-only client)
	code := r.URL.Query().Get("code")
	exchangeCtx := context.WithValue(r.Context(), oauth2.HTTPClient, a.httpClient)
	token, err := kc.oauth2Config.Exchange(exchangeCtx, code)
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
	idToken, err := kc.verifier.Verify(r.Context(), rawIDToken)
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
		GivenName     string `json:"given_name"`
		FamilyName    string `json:"family_name"`
		PreferredName string `json:"preferred_username"`
		Locale        string `json:"locale"`
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
		GivenName:     claims.GivenName,
		FamilyName:    claims.FamilyName,
		PreferredName: claims.PreferredName,
		Roles:         roles,
		Locale:        claims.Locale,
	}

	// Fetch user's Keycloak applications using the fresh access token
	// This is cached server-side and does not block login on failure
	if apps, err := a.fetchUserApplications(r.Context(), token.AccessToken); err != nil {
		fmt.Printf("⚠ WARNING: Failed to fetch user applications: %v\n", err)
	} else {
		a.appCacheMu.Lock()
		a.appCache[user.ID] = apps
		a.appCacheMu.Unlock()
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

	// Redirect to profile
	http.Redirect(w, r, "/profile", http.StatusTemporaryRedirect)
}

// LogoutHandler clears the session and redirects to Keycloak logout
func (a *Authenticator) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	// Evict cached applications before clearing session
	if user := a.GetUser(r); user != nil {
		a.appCacheMu.Lock()
		delete(a.appCache, user.ID)
		a.appCacheMu.Unlock()
	}

	session, _ := a.store.Get(r, sessionName)
	session.Values = make(map[interface{}]interface{})
	session.Options.MaxAge = -1
	session.Save(r, w)

	// Redirect to Keycloak end_session_endpoint to log out from SSO
	if a.keycloak() != nil {
		logoutURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/logout?post_logout_redirect_uri=%s&client_id=%s",
			a.config.KeycloakURL,
			a.config.KeycloakRealm,
			a.config.BaseURL,
			a.config.KeycloakClientID,
		)
		http.Redirect(w, r, logoutURL, http.StatusTemporaryRedirect)
		return
	}

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

// GetUserApplications returns cached Keycloak applications for a user
func (a *Authenticator) GetUserApplications(userID string) []KeycloakApplication {
	a.appCacheMu.RLock()
	defer a.appCacheMu.RUnlock()
	apps := a.appCache[userID]
	if len(apps) == 0 {
		return nil
	}
	result := make([]KeycloakApplication, len(apps))
	copy(result, apps)
	return result
}

// fetchUserApplications calls Keycloak Account API to get applications visible to the user.
// It filters out internal clients and clients without URLs.
func (a *Authenticator) fetchUserApplications(ctx context.Context, accessToken string) ([]KeycloakApplication, error) {
	url := fmt.Sprintf("%s/realms/%s/account/applications", a.config.KeycloakURL, a.config.KeycloakRealm)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling account API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("account API returned %d: %s", resp.StatusCode, string(body))
	}

	var allApps []KeycloakApplication
	if err := json.NewDecoder(resp.Body).Decode(&allApps); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	// Filter: skip internal clients and clients without usable URLs
	skipClients := map[string]bool{
		a.config.KeycloakClientID: true, // our own portal client
		"account":                 true,
		"account-console":         true,
		"admin-cli":               true,
		"broker":                  true,
		"realm-management":        true,
		"security-admin-console":  true,
	}

	var apps []KeycloakApplication
	for _, app := range allApps {
		if skipClients[app.ClientID] {
			continue
		}
		if app.URL() == "" {
			continue
		}
		apps = append(apps, app)
	}

	sort.Slice(apps, func(i, j int) bool {
		return apps[i].DisplayName() < apps[j].DisplayName()
	})

	return apps, nil
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
