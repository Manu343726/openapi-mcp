package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_GetAPIKey(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		envKey      string // Environment variable name to set
		envValue    string // Value to set for the env var
		expectedKey string
		cleanupEnv  bool // Flag to indicate if env var needs cleanup
	}{
		{
			name:        "No key set",
			config:      Config{}, // Empty config
			expectedKey: "",
		},
		{
			name: "Direct key set only",
			config: Config{
				APIKey: "direct-key-123",
			},
			expectedKey: "direct-key-123",
		},
		{
			name: "Env var set only",
			config: Config{
				APIKeyFromEnvVar: "TEST_API_KEY_ENV_ONLY",
			},
			envKey:      "TEST_API_KEY_ENV_ONLY",
			envValue:    "env-key-456",
			expectedKey: "env-key-456",
			cleanupEnv:  true,
		},
		{
			name: "Both direct and env var set (env takes precedence)",
			config: Config{
				APIKey:           "direct-key-789",
				APIKeyFromEnvVar: "TEST_API_KEY_BOTH",
			},
			envKey:      "TEST_API_KEY_BOTH",
			envValue:    "env-key-abc",
			expectedKey: "env-key-abc",
			cleanupEnv:  true,
		},
		{
			name: "Direct key set, env var specified but not set",
			config: Config{
				APIKey:           "direct-key-xyz",
				APIKeyFromEnvVar: "TEST_API_KEY_UNSET",
			},
			envKey:      "TEST_API_KEY_UNSET", // Ensure this is not set
			envValue:    "",
			expectedKey: "direct-key-xyz", // Should fall back to direct key
			cleanupEnv:  true,             // Cleanup in case it was set previously
		},
		{
			name: "Env var specified but empty string value",
			config: Config{
				APIKeyFromEnvVar: "TEST_API_KEY_EMPTY",
			},
			envKey:      "TEST_API_KEY_EMPTY",
			envValue:    "", // Explicitly set to empty string
			expectedKey: "", // Empty env var should result in empty key
			cleanupEnv:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set environment variable if needed for this test case
			if tc.envKey != "" {
				originalValue, wasSet := os.LookupEnv(tc.envKey)
				err := os.Setenv(tc.envKey, tc.envValue)
				if err != nil {
					t.Fatalf("Failed to set environment variable %s: %v", tc.envKey, err)
				}
				// Schedule cleanup
				if tc.cleanupEnv {
					t.Cleanup(func() {
						if wasSet {
							os.Setenv(tc.envKey, originalValue)
						} else {
							os.Unsetenv(tc.envKey)
						}
					})
				}
			} else {
				// Ensure env var is unset if tc.envKey is empty (for tests like "Direct key set only")
				// This prevents interference from previous tests if not cleaned up properly.
				os.Unsetenv(tc.config.APIKeyFromEnvVar) // Unset based on config field if relevant
			}

			// Call the method under test
			actualKey := tc.config.GetAPIKey()

			// Assert the result
			if actualKey != tc.expectedKey {
				t.Errorf("Expected API key %q, but got %q", tc.expectedKey, actualKey)
			}
		})
	}
}

func TestFileConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	fc := &FileConfig{APIs: []APIDefinition{
		{
			Name:         "weather",
			Source:       "https://spec.example.com/weather.json",
			ActiveTarget: "prod",
			IncludeTags:  []string{"public"},
			Auth: AuthConfig{
				Type: AuthAPIKey,
				In:   "query",
				Name: "key",
			},
			Targets: []TargetDefinition{
				{Name: "prod", BaseURL: "https://prod.example.com", APIKeyEnv: "W_KEY"},
				{Name: "staging", BaseURL: "https://staging.example.com"},
			},
		},
	}}

	require.NoError(t, SaveFile(path, fc))
	// The persisted file must be YAML, not JSON.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"apis":`)

	loaded, err := LoadFile(path)
	require.NoError(t, err)
	require.Len(t, loaded.APIs, 1)
	got := loaded.APIs[0]
	assert.Equal(t, "weather", got.Name)
	assert.Equal(t, "prod", got.ActiveTarget)
	assert.Equal(t, "https://prod.example.com", got.Targets[0].BaseURL)
	assert.Equal(t, "query", got.Auth.Effective().In)
	assert.Equal(t, "query", got.Auth.In)

	cfg := got.Targets[0].ToConfig()
	assert.Equal(t, "https://prod.example.com", cfg.ServerBaseURL)
	assert.Equal(t, "W_KEY", cfg.APIKeyFromEnvVar)

	assert.False(t, got.Targets[0].InsecureSkipVerify)
	assert.False(t, cfg.InsecureSkipVerify)
}

func TestFileConfigLoadsJSON(t *testing.T) {
	// yaml.v3 also parses a plain JSON file, so legacy configs keep working.
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"apis":[{"name":"legacy","spec":"{}"}]}`), 0644)
	fc, err := LoadFile(path)
	require.NoError(t, err)
	require.Len(t, fc.APIs, 1)
	assert.Equal(t, "legacy", fc.APIs[0].Name)
}

func TestToConfigCustomHeaders(t *testing.T) {
	target := TargetDefinition{
		CustomHeaders: map[string]string{"X-Trace": "abc", "X-Tenant": "t1"},
	}
	cfg := target.ToConfig()
	assert.Contains(t, cfg.CustomHeaders, "X-Trace:abc")
	assert.Contains(t, cfg.CustomHeaders, "X-Tenant:t1")
}

func TestLoadFileMissingAndInvalid(t *testing.T) {
	_, err := LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	assert.ErrorContains(t, err, "reading config file")

	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte("{not json"), 0644)
	_, err = LoadFile(path)
	assert.ErrorContains(t, err, "parsing config file")
}
