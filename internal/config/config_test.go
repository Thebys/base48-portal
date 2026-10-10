package config

import "testing"

func TestKeycloakReadOnly(t *testing.T) {
	required := map[string]string{
		"KEYCLOAK_URL":           "https://sso.example",
		"KEYCLOAK_REALM":         "hackerspace",
		"KEYCLOAK_CLIENT_ID":     "portal",
		"KEYCLOAK_CLIENT_SECRET": "secret",
		"SESSION_SECRET":         "session",
	}

	tests := []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{"", false, false},
		{"true", true, false},
		{"TRUE", true, false},
		{"1", true, false},
		{"false", false, false},
		{"0", false, false},
		// Values getEnvBool would read as false: refused, not ignored.
		{"on", false, true},
		{"yes", false, true},
		{"ture", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			for k, v := range required {
				t.Setenv(k, v)
			}
			t.Setenv("KEYCLOAK_READ_ONLY", tt.value)

			cfg, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("KEYCLOAK_READ_ONLY=%q loaded without error (read-only = %v)", tt.value, cfg.KeycloakReadOnly)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.KeycloakReadOnly != tt.want {
				t.Errorf("KEYCLOAK_READ_ONLY=%q gives read-only = %v, want %v", tt.value, cfg.KeycloakReadOnly, tt.want)
			}
		})
	}
}
