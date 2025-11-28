package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/base48/member-portal/internal/config"
)

// ServiceAccountClient handles Keycloak service account authentication
type ServiceAccountClient struct {
	config       *config.Config
	oauth2Config *oauth2.Config
	tokenSource  oauth2.TokenSource
	username     string
	password     string
}

// NewServiceAccountClient creates a new service account client using client credentials flow
// (requires confidential client with service account enabled)
func NewServiceAccountClient(ctx context.Context, cfg *config.Config, clientID, clientSecret string) (*ServiceAccountClient, error) {
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("service account client ID and secret are required")
	}

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		cfg.KeycloakURL, cfg.KeycloakRealm)

	// Create HTTP client with aggressive timeouts
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

	oauth2Config := clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		Scopes:       []string{"openid", "profile", "email"},
	}

	// Use custom HTTP client with timeout
	ctxWithClient := context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	tokenSource := oauth2Config.TokenSource(ctxWithClient)

	// Test the connection by getting a token
	_, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate service account: %w", err)
	}

	return &ServiceAccountClient{
		config:      cfg,
		tokenSource: tokenSource,
	}, nil
}

// NewServiceAccountClientWithPassword creates a service account using username/password
// (Resource Owner Password Credentials flow)
func NewServiceAccountClientWithPassword(ctx context.Context, cfg *config.Config, clientID, username, password string) (*ServiceAccountClient, error) {
	if clientID == "" || username == "" || password == "" {
		return nil, fmt.Errorf("client ID, username, and password are required")
	}

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		cfg.KeycloakURL, cfg.KeycloakRealm)

	oauth2Config := &oauth2.Config{
		ClientID: clientID,
		Endpoint: oauth2.Endpoint{
			TokenURL: tokenURL,
		},
		Scopes: []string{"openid", "profile", "email"},
	}

	// Get initial token using password grant
	token, err := oauth2Config.PasswordCredentialsToken(ctx, username, password)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with username/password: %w", err)
	}

	tokenSource := oauth2Config.TokenSource(ctx, token)

	return &ServiceAccountClient{
		config:       cfg,
		oauth2Config: oauth2Config,
		tokenSource:  tokenSource,
		username:     username,
		password:     password,
	}, nil
}

// GetAccessToken returns a valid access token (automatically refreshes if needed)
func (s *ServiceAccountClient) GetAccessToken(ctx context.Context) (string, error) {
	token, err := s.tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}
	return token.AccessToken, nil
}

// IsTokenValid checks if the current token is still valid
func (s *ServiceAccountClient) IsTokenValid(ctx context.Context) bool {
	token, err := s.tokenSource.Token()
	if err != nil {
		return false
	}
	return token.Valid()
}
