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
	if cfg.RedisURL != "redis://127.0.0.1:6379/0" {
		t.Errorf("RedisURL = %q, want the documented local default", cfg.RedisURL)
	}
	if cfg.HoldTTL != 10*time.Minute {
		t.Errorf("HoldTTL = %v, want 10m", cfg.HoldTTL)
	}
	if cfg.ExpiryInterval != 30*time.Second {
		t.Errorf("ExpiryInterval = %v, want the documented 30s default", cfg.ExpiryInterval)
	}
	if cfg.ExpiryBatch != 100 {
		t.Errorf("ExpiryBatch = %d, want the documented 100 default", cfg.ExpiryBatch)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"API_ADDR":                 "127.0.0.1:9090",
		"API_LOG_LEVEL":            "debug",
		"API_SHUTDOWN_TIMEOUT_MS":  "3000",
		"API_READINESS_TIMEOUT_MS": "500",
		"API_DEPENDENCIES":         " postgresql , redis ,, ",
		"API_REDIS_URL":            "redis://127.0.0.1:6390/9",
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
	if cfg.RedisURL != "redis://127.0.0.1:6390/9" {
		t.Errorf("RedisURL = %q, want the configured URL", cfg.RedisURL)
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

// TestExpiryConfiguration pins the durable sweep rules: a documented cadence and
// batch default, seconds allowed for tests like API_HOLD_TTL, and a cadence that
// can never exceed half the hold window, so the sweep always fires at least twice
// inside one hold before the window closes.
func TestExpiryConfiguration(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		wantInterval time.Duration
		wantBatch    int
		wantError    string
	}{
		{
			name:         "defaults documented in the code",
			wantInterval: 30 * time.Second,
			wantBatch:    100,
		},
		{
			name:         "seconds allowed for tests",
			env:          map[string]string{"API_EXPIRY_INTERVAL": "5s"},
			wantInterval: 5 * time.Second,
			wantBatch:    100,
		},
		{
			name:         "sub-second cadence allowed",
			env:          map[string]string{"API_EXPIRY_INTERVAL": "250ms"},
			wantInterval: 250 * time.Millisecond,
			wantBatch:    100,
		},
		{
			name:         "batch override",
			env:          map[string]string{"API_EXPIRY_BATCH": "25"},
			wantInterval: 30 * time.Second,
			wantBatch:    25,
		},
		{
			name:         "a short hold window lowers the default cadence",
			env:          map[string]string{"API_HOLD_TTL": "1s"},
			wantInterval: 500 * time.Millisecond,
			wantBatch:    100,
		},
		{
			name:         "cadence exactly half the window is accepted",
			env:          map[string]string{"API_HOLD_TTL": "10s", "API_EXPIRY_INTERVAL": "5s"},
			wantInterval: 5 * time.Second,
			wantBatch:    100,
		},
		{
			name:         "the documented cadence clamps to half a longer window",
			env:          map[string]string{"API_HOLD_TTL": "40s"},
			wantInterval: 20 * time.Second,
			wantBatch:    100,
		},
		{
			name:      "a window too short to schedule any sweep is rejected",
			env:       map[string]string{"API_HOLD_TTL": "1ns"},
			wantError: "API_EXPIRY_INTERVAL",
		},
		{
			name:      "cadence above half the window is rejected",
			env:       map[string]string{"API_HOLD_TTL": "10s", "API_EXPIRY_INTERVAL": "6s"},
			wantError: "API_EXPIRY_INTERVAL",
		},
		{
			name:      "unparseable cadence rejected",
			env:       map[string]string{"API_EXPIRY_INTERVAL": "soon"},
			wantError: "API_EXPIRY_INTERVAL",
		},
		{
			name:      "zero cadence rejected",
			env:       map[string]string{"API_EXPIRY_INTERVAL": "0s"},
			wantError: "API_EXPIRY_INTERVAL",
		},
		{
			name:      "negative cadence rejected",
			env:       map[string]string{"API_EXPIRY_INTERVAL": "-1s"},
			wantError: "API_EXPIRY_INTERVAL",
		},
		{
			name:      "zero batch rejected",
			env:       map[string]string{"API_EXPIRY_BATCH": "0"},
			wantError: "API_EXPIRY_BATCH",
		},
		{
			name:      "negative batch rejected",
			env:       map[string]string{"API_EXPIRY_BATCH": "-3"},
			wantError: "API_EXPIRY_BATCH",
		},
		{
			name:      "non numeric batch rejected",
			env:       map[string]string{"API_EXPIRY_BATCH": "lots"},
			wantError: "API_EXPIRY_BATCH",
		},
		{
			name:      "oversized batch rejected",
			env:       map[string]string{"API_EXPIRY_BATCH": "100000"},
			wantError: "API_EXPIRY_BATCH",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(func(key string) string { return tc.env[key] })
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("expected an error naming %s, got nil", tc.wantError)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %q, want it to name %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.ExpiryInterval != tc.wantInterval {
				t.Errorf("ExpiryInterval = %v, want %v", cfg.ExpiryInterval, tc.wantInterval)
			}
			if cfg.ExpiryBatch != tc.wantBatch {
				t.Errorf("ExpiryBatch = %d, want %d", cfg.ExpiryBatch, tc.wantBatch)
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

// TestHoldTTL pins the hold window rule: ten minutes by default, overridable
// with any Go duration so tests can use seconds, and rejected when it does not
// parse or is not positive.
func TestHoldTTL(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		want      time.Duration
		wantError bool
	}{
		{"default is ten minutes", "", 10 * time.Minute, false},
		{"seconds allowed for tests", "30s", 30 * time.Second, false},
		{"minutes allowed", "15m", 15 * time.Minute, false},
		{"milliseconds allowed", "1500ms", 1500 * time.Millisecond, false},
		{"unparseable value rejected", "ten minutes", 0, true},
		{"missing unit rejected", "30", 0, true},
		{"zero rejected", "0s", 0, true},
		{"negative rejected", "-5s", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			if tc.value != "" {
				env["API_HOLD_TTL"] = tc.value
			}
			cfg, err := config.LoadFrom(func(key string) string { return env[key] })
			if tc.wantError {
				if err == nil {
					t.Fatalf("HoldTTL = %v, want an error", cfg.HoldTTL)
				}
				if !strings.Contains(err.Error(), "API_HOLD_TTL") {
					t.Fatalf("error = %q, want it to name API_HOLD_TTL", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.HoldTTL != tc.want {
				t.Errorf("HoldTTL = %v, want %v", cfg.HoldTTL, tc.want)
			}
		})
	}
}

// The coordination URL is trimmed, so an environment file with padding still
// yields a parseable URL rather than a startup failure.
func TestRedisURLIsTrimmed(t *testing.T) {
	env := map[string]string{"API_REDIS_URL": "  redis://127.0.0.1:6379/1  "}
	cfg, err := config.LoadFrom(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}
	if cfg.RedisURL != "redis://127.0.0.1:6379/1" {
		t.Fatalf("RedisURL = %q, want it trimmed", cfg.RedisURL)
	}
}
