package config

import (
	"fmt"
	"os"
)

type Config struct {
	// Server
	Port    string
	BaseURL string

	// Database
	DatabaseURL string

	// Keycloak
	KeycloakURL          string
	KeycloakRealm        string
	KeycloakClientID     string
	KeycloakClientSecret string

	// Keycloak Service Account (for automated tasks)
	KeycloakServiceAccountClientID     string
	KeycloakServiceAccountClientSecret string

	// Session
	SessionSecret string
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:                               getEnv("PORT", "8080"),
		BaseURL:                            getEnv("BASE_URL", "http://localhost:8080"),
		DatabaseURL:                        getEnv("DATABASE_URL", "file:./data/portal.db?_fk=1"),
		KeycloakURL:                        getEnv("KEYCLOAK_URL", ""),
		KeycloakRealm:                      getEnv("KEYCLOAK_REALM", ""),
		KeycloakClientID:                   getEnv("KEYCLOAK_CLIENT_ID", ""),
		KeycloakClientSecret:               getEnv("KEYCLOAK_CLIENT_SECRET", ""),
		KeycloakServiceAccountClientID:     getEnv("KEYCLOAK_SERVICE_ACCOUNT_CLIENT_ID", ""),
		KeycloakServiceAccountClientSecret: getEnv("KEYCLOAK_SERVICE_ACCOUNT_CLIENT_SECRET", ""),
		SessionSecret:                      getEnv("SESSION_SECRET", ""),
	}

	// Validate required fields
	if cfg.KeycloakURL == "" {
		return nil, fmt.Errorf("KEYCLOAK_URL is required")
	}
	if cfg.KeycloakRealm == "" {
		return nil, fmt.Errorf("KEYCLOAK_REALM is required")
	}
	if cfg.KeycloakClientID == "" {
		return nil, fmt.Errorf("KEYCLOAK_CLIENT_ID is required")
	}
	if cfg.KeycloakClientSecret == "" {
		return nil, fmt.Errorf("KEYCLOAK_CLIENT_SECRET is required")
	}
	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("SESSION_SECRET is required")
	}

	return cfg, nil
}

func (c *Config) KeycloakIssuerURL() string {
	return fmt.Sprintf("%s/realms/%s", c.KeycloakURL, c.KeycloakRealm)
}

func (c *Config) OAuthCallbackURL() string {
	return fmt.Sprintf("%s/auth/callback", c.BaseURL)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
