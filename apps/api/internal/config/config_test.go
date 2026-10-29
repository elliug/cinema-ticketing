package config_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.LoadFrom(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
	if cfg.ReadinessTimeout != 5*time.Second {
		t.Errorf("ReadinessTimeout = %v, want 5s", cfg.ReadinessTimeout)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if !slices.Equal(cfg.Dependencies, []string{}) {
		t.Errorf("Dependencies = %q, want []", cfg.Dependencies)
	}
	if cfg.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty by default", cfg.DatabaseURL)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"API_ADDR":                 "127.0.0.1:9090",
		"API_LOG_LEVEL":            "debug",
		"API_SHUTDOWN_TIMEOUT_MS":  "3000",
		"API_READINESS_TIMEOUT_MS": "500",
		"API_DEPENDENCIES":         " postgresql , redis ,, ",
	}
	cfg, err := config.LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" {
		t.Errorf("Addr = %q, want 127.0.0.1:9090", cfg.Addr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 3s", cfg.ShutdownTimeout)
	}
	if cfg.ReadinessTimeout != 500*time.Millisecond {
		t.Errorf("ReadinessTimeout = %v, want 500ms", cfg.ReadinessTimeout)
	}
	if len(cfg.Dependencies) != 2 || cfg.Dependencies[0] != "postgresql" || cfg.Dependencies[1] != "redis" {
		t.Errorf("Dependencies = %q, want [postgresql redis]", cfg.Dependencies)
	}
}

func TestLocalAdminConfiguration(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		wantErr     bool
		wantEnabled bool
	}{
		{"default disabled", map[string]string{}, false, false},
		{"production denied", map[string]string{"API_ENV": "production", "API_LOCAL_ADMIN": "true", "API_LOCAL_ADMIN_TOKEN": "development-token"}, true, false},
		{"unknown environment denied", map[string]string{"API_LOCAL_ADMIN": "true", "API_LOCAL_ADMIN_TOKEN": "development-token"}, true, false},
		{"missing token denied", map[string]string{"API_ENV": "development", "API_LOCAL_ADMIN": "true", "API_DATABASE_URL": testDatabaseURL}, true, false},
		{"local development enabled", map[string]string{"API_ENV": "development", "API_LOCAL_ADMIN": "true", "API_LOCAL_ADMIN_TOKEN": "development-token", "API_DATABASE_URL": testDatabaseURL}, false, true},
		{"invalid switch denied", map[string]string{"API_LOCAL_ADMIN": "yes"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(func(key string) string { return tc.env[key] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error = %v", err, tc.wantErr)
			}
			if err == nil && cfg.LocalAdminEnabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", cfg.LocalAdminEnabled, tc.wantEnabled)
			}
		})
	}
}

// testDatabaseURL is a development-only placeholder; no connection is made.
const testDatabaseURL = "postgres://cinema@127.0.0.1:5432/cinema?sslmode=disable"

// TestLocalAdminDatabaseURL pins the API_DATABASE_URL rules: it is required when
// local administration is enabled, optional otherwise, and never echoed in an
// error message.
func TestLocalAdminDatabaseURL(t *testing.T) {
	localAdmin := map[string]string{
		"API_ENV":               "development",
		"API_LOCAL_ADMIN":       "true",
		"API_LOCAL_ADMIN_TOKEN": "development-token",
	}
	withLocalAdmin := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for key, value := range localAdmin {
			env[key] = value
		}
		for key, value := range extra {
			env[key] = value
		}
		return env
	}

	cases := []struct {
		name         string
		env          map[string]string
		wantErr      bool
		wantDatabase string
	}{
		{
			name:    "enabled local administration requires a database URL",
			env:     withLocalAdmin(nil),
			wantErr: true,
		},
		{
			name:    "enabled local administration rejects an empty database URL",
			env:     withLocalAdmin(map[string]string{"API_DATABASE_URL": ""}),
			wantErr: true,
		},
		{
			name:    "enabled local administration rejects a blank database URL",
			env:     withLocalAdmin(map[string]string{"API_DATABASE_URL": "   "}),
			wantErr: true,
		},
		{
			name:         "enabled local administration accepts a database URL",
			env:          withLocalAdmin(map[string]string{"API_DATABASE_URL": testDatabaseURL}),
			wantDatabase: testDatabaseURL,
		},
		{
			name:         "enabled local administration normalizes a padded database URL",
			env:          withLocalAdmin(map[string]string{"API_DATABASE_URL": "  " + testDatabaseURL + "\t"}),
			wantDatabase: testDatabaseURL,
		},
		{
			name: "disabled local administration stays valid without a database URL",
			env:  map[string]string{},
		},
		{
			name:         "disabled local administration keeps a configured database URL",
			env:          map[string]string{"API_DATABASE_URL": testDatabaseURL},
			wantDatabase: testDatabaseURL,
		},
		{
			name: "production local administration is rejected even with a database URL",
			env: map[string]string{
				"API_ENV":               "production",
				"API_LOCAL_ADMIN":       "true",
				"API_LOCAL_ADMIN_TOKEN": "development-token",
				"API_DATABASE_URL":      testDatabaseURL,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(func(key string) string { return tc.env[key] })
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				// The database URL may carry credentials, so it must never be echoed.
				if strings.Contains(err.Error(), "postgres://") {
					t.Fatalf("error message leaked the database URL: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.DatabaseURL != tc.wantDatabase {
				t.Fatalf("DatabaseURL = %q, want %q", cfg.DatabaseURL, tc.wantDatabase)
			}
		})
	}
}

func TestLoadInvalidShutdownTimeout(t *testing.T) {
	_, err := config.LoadFrom(func(k string) string {
		if k == "API_SHUTDOWN_TIMEOUT_MS" {
			return "notanumber"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error for invalid API_SHUTDOWN_TIMEOUT_MS, got nil")
	}
}

func TestLoadInvalidReadinessTimeout(t *testing.T) {
	_, err := config.LoadFrom(func(k string) string {
		if k == "API_READINESS_TIMEOUT_MS" {
			return "abc"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error for invalid API_READINESS_TIMEOUT_MS, got nil")
	}
}
