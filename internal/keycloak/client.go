package keycloak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/base48/member-portal/internal/config"
)

// Client wraps Keycloak Admin API calls
type Client struct {
	config      *config.Config
	adminToken  string
	httpClient  *http.Client
}

// Role represents a Keycloak role
type Role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Composite   bool   `json:"composite"`
	ClientRole  bool   `json:"clientRole"`
	ContainerID string `json:"containerId"`
}

// NewClient creates a new Keycloak admin client
func NewClient(cfg *config.Config, adminToken string) *Client {
	return &Client{
		config:     cfg,
		adminToken: adminToken,
		httpClient: &http.Client{},
	}
}

// GetRealmRoles returns all realm roles
func (c *Client) GetRealmRoles(ctx context.Context) ([]Role, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/roles", c.config.KeycloakURL, c.config.KeycloakRealm)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get roles: %s - %s", resp.Status, string(body))
	}

	var roles []Role
	if err := json.NewDecoder(resp.Body).Decode(&roles); err != nil {
		return nil, err
	}

	return roles, nil
}

// GetRoleByName gets a specific realm role by name
func (c *Client) GetRoleByName(ctx context.Context, roleName string) (*Role, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/roles/%s", c.config.KeycloakURL, c.config.KeycloakRealm, roleName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get role: %s - %s", resp.Status, string(body))
	}

	var role Role
	if err := json.NewDecoder(resp.Body).Decode(&role); err != nil {
		return nil, err
	}

	return &role, nil
}

// GetUserRoles returns all realm roles assigned to a user
func (c *Client) GetUserRoles(ctx context.Context, userID string) ([]Role, error) {
	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm",
		c.config.KeycloakURL, c.config.KeycloakRealm, userID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get user roles: %s - %s", resp.Status, string(body))
	}

	var roles []Role
	if err := json.NewDecoder(resp.Body).Decode(&roles); err != nil {
		return nil, err
	}

	return roles, nil
}

// AssignRoleToUser assigns a realm role to a user
func (c *Client) AssignRoleToUser(ctx context.Context, userID, roleName string) error {
	// First get the role details
	role, err := c.GetRoleByName(ctx, roleName)
	if err != nil {
		return fmt.Errorf("failed to get role %s: %w", roleName, err)
	}

	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm",
		c.config.KeycloakURL, c.config.KeycloakRealm, userID)

	// Keycloak expects an array of role objects
	rolePayload := []Role{*role}
	body, err := json.Marshal(rolePayload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to assign role: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// RemoveRoleFromUser removes a realm role from a user
func (c *Client) RemoveRoleFromUser(ctx context.Context, userID, roleName string) error {
	// First get the role details
	role, err := c.GetRoleByName(ctx, roleName)
	if err != nil {
		return fmt.Errorf("failed to get role %s: %w", roleName, err)
	}

	url := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm",
		c.config.KeycloakURL, c.config.KeycloakRealm, userID)

	// Keycloak expects an array of role objects
	rolePayload := []Role{*role}
	body, err := json.Marshal(rolePayload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to remove role: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// UserHasRole checks if a user has a specific role
func (c *Client) UserHasRole(ctx context.Context, userID, roleName string) (bool, error) {
	roles, err := c.GetUserRoles(ctx, userID)
	if err != nil {
		return false, err
	}

	for _, role := range roles {
		if role.Name == roleName {
			return true, nil
		}
	}

	return false, nil
}
