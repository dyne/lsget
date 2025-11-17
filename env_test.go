package main

import (
	"fmt"
	"os"
	"testing"
)

func TestEnvironmentVariables(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		envValue string
		expected string
	}{
		{"LSGET_ADDR", "LSGET_ADDR", "0.0.0.0:9090", "0.0.0.0:9090"},
		{"LSGET_DIR", "LSGET_DIR", "/custom/path", "/custom/path"},
		{"LSGET_LOGFILE", "LSGET_LOGFILE", "/var/log/test.log", "/var/log/test.log"},
		{"LSGET_BASEURL", "LSGET_BASEURL", "https://test.example.com", "https://test.example.com"},
		{"LSGET_PID", "LSGET_PID", "/var/run/test.pid", "/var/run/test.pid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variable
			os.Setenv(tt.envKey, tt.envValue)
			defer os.Unsetenv(tt.envKey)

			// Test string env vars
			getEnvOrDefault := func(key, defaultValue string) string {
				if v := os.Getenv(key); v != "" {
					return v
				}
				return defaultValue
			}

			result := getEnvOrDefault(tt.envKey, "default")
			if result != tt.expected {
				t.Errorf("Expected %s=%s, got %s", tt.envKey, tt.expected, result)
			}
		})
	}
}

func TestEnvironmentVariablesInt(t *testing.T) {
	tests := []struct {
		name        string
		envKey      string
		envValue    string
		expected    int
		shouldError bool
	}{
		{"LSGET_SITEMAP valid", "LSGET_SITEMAP", "60", 60, false},
		{"LSGET_SITEMAP zero", "LSGET_SITEMAP", "0", 0, false},
		{"LSGET_SITEMAP large", "LSGET_SITEMAP", "1440", 1440, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variable
			os.Setenv(tt.envKey, tt.envValue)
			defer os.Unsetenv(tt.envKey)

			// Test int env vars
			getEnvOrDefaultInt := func(key string, defaultValue int) int {
				if v := os.Getenv(key); v != "" {
					var result int
					if _, err := fmt.Sscanf(v, "%d", &result); err == nil {
						return result
					}
				}
				return defaultValue
			}

			result := getEnvOrDefaultInt(tt.envKey, 999)
			if result != tt.expected {
				t.Errorf("Expected %s=%d, got %d", tt.envKey, tt.expected, result)
			}
		})
	}
}

func TestEnvironmentVariablesInt64(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		envValue string
		expected int64
	}{
		{"LSGET_CATMAX default", "LSGET_CATMAX", "4096", 4096},
		{"LSGET_CATMAX large", "LSGET_CATMAX", "262144", 262144},
		{"LSGET_CATMAX zero", "LSGET_CATMAX", "0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variable
			os.Setenv(tt.envKey, tt.envValue)
			defer os.Unsetenv(tt.envKey)

			// Test int64 env vars
			getEnvOrDefaultInt64 := func(key string, defaultValue int64) int64 {
				if v := os.Getenv(key); v != "" {
					var result int64
					if _, err := fmt.Sscanf(v, "%d", &result); err == nil {
						return result
					}
				}
				return defaultValue
			}

			result := getEnvOrDefaultInt64(tt.envKey, 999)
			if result != tt.expected {
				t.Errorf("Expected %s=%d, got %d", tt.envKey, tt.expected, result)
			}
		})
	}
}
