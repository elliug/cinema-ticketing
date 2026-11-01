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
		{
			name:      "a sub-second presign lifetime is rejected",
			env:       map[string]string{"API_S3_PRESIGN_TTL": "500ms"},
			wantError: "API_S3_PRESIGN_TTL",
		},
		{
			name:      "a compose-network endpoint with the dummy pair is rejected",
			env:       map[string]string{"API_S3_ENDPOINT": "https://floci:4566", "API_S3_ACCESS_KEY_ID": "floci", "API_S3_SECRET_ACCESS_KEY": "floci"},
			wantError: "API_S3_ACCESS_KEY_ID",
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
			// A configured database URL means this process can serve tickets, so the
			// development environment (and with it the documented QR key) is required;
			// see TestTicketQRKeyConfiguration.
			name:         "disabled local administration keeps a configured database URL",
			env:          map[string]string{"API_ENV": "development", "API_DATABASE_URL": testDatabaseURL},
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

// TestS3Configuration pins the object-storage rules: the documented defaults that
// make a local Floci container work with no environment variables at all, and the
// combinations that cannot work, which are refused instead of started.
func TestS3Configuration(t *testing.T) {
	const secret = "local-floci-secret"

	cases := []struct {
		name          string
		env           map[string]string
		wantEndpoint  string
		wantBucket    string
		wantRegion    string
		wantAccessKey string
		wantSecret    string
		wantPresign   time.Duration
		wantError     string
	}{
		{
			name:          "defaults documented in the code",
			wantEndpoint:  "http://127.0.0.1:4566",
			wantBucket:    "cinema-m3-tickets",
			wantRegion:    "us-east-1",
			wantAccessKey: "floci",
			wantSecret:    "floci",
			wantPresign:   5 * time.Minute,
		},
		{
			name: "every setting overridden",
			env: map[string]string{
				"API_S3_ENDPOINT":          "https://objects.example.test",
				"API_S3_BUCKET":            "cinema-m3-production",
				"API_S3_REGION":            "eu-west-1",
				"API_S3_ACCESS_KEY_ID":     "AKIAEXAMPLE",
				"API_S3_SECRET_ACCESS_KEY": secret,
				"API_S3_PRESIGN_TTL":       "90s",
			},
			wantEndpoint:  "https://objects.example.test",
			wantBucket:    "cinema-m3-production",
			wantRegion:    "eu-west-1",
			wantAccessKey: "AKIAEXAMPLE",
			wantSecret:    secret,
			wantPresign:   90 * time.Second,
		},
		{
			name: "a trailing slash on the endpoint is trimmed",
			env: map[string]string{
				"API_S3_ENDPOINT": " http://127.0.0.1:9000/ ",
			},
			wantEndpoint:  "http://127.0.0.1:9000",
			wantBucket:    "cinema-m3-tickets",
			wantRegion:    "us-east-1",
			wantAccessKey: "floci",
			wantSecret:    "floci",
			wantPresign:   5 * time.Minute,
		},
		{
			name:      "an endpoint without a scheme is rejected",
			env:       map[string]string{"API_S3_ENDPOINT": "127.0.0.1:4566"},
			wantError: "API_S3_ENDPOINT",
		},
		{
			name:      "an endpoint with a path is rejected",
			env:       map[string]string{"API_S3_ENDPOINT": "http://127.0.0.1:4566/prefix"},
			wantError: "API_S3_ENDPOINT",
		},
		{
			name:      "an endpoint with a query is rejected",
			env:       map[string]string{"API_S3_ENDPOINT": "http://127.0.0.1:4566?x=1"},
			wantError: "API_S3_ENDPOINT",
		},
		{
			name:      "an unsupported endpoint scheme is rejected",
			env:       map[string]string{"API_S3_ENDPOINT": "ftp://127.0.0.1:4566"},
			wantError: "API_S3_ENDPOINT",
		},
		{
			name:      "an upper case bucket is rejected",
			env:       map[string]string{"API_S3_BUCKET": "Cinema-M3-Tickets"},
			wantError: "API_S3_BUCKET",
		},
		{
			name:      "an underscore in the bucket is rejected",
			env:       map[string]string{"API_S3_BUCKET": "cinema_m3_tickets"},
			wantError: "API_S3_BUCKET",
		},
		{
			name:      "a bucket that starts with a hyphen is rejected",
			env:       map[string]string{"API_S3_BUCKET": "-cinema-m3"},
			wantError: "API_S3_BUCKET",
		},
		{
			name:      "a too short bucket is rejected",
			env:       map[string]string{"API_S3_BUCKET": "m3"},
			wantError: "API_S3_BUCKET",
		},
		{
			name:      "a blank region is rejected",
			env:       map[string]string{"API_S3_REGION": "   "},
			wantError: "API_S3_REGION",
		},
		{
			name:      "an access key without a secret is rejected",
			env:       map[string]string{"API_S3_ACCESS_KEY_ID": "AKIAEXAMPLE"},
			wantError: "API_S3_SECRET_ACCESS_KEY",
		},
		{
			name:      "a secret without an access key is rejected",
			env:       map[string]string{"API_S3_SECRET_ACCESS_KEY": secret},
			wantError: "API_S3_ACCESS_KEY_ID",
		},
		{
			name: "the development credential pair is rejected for a remote endpoint",
			env: map[string]string{
				"API_S3_ENDPOINT": "https://objects.example.test",
			},
			wantError: "API_S3_ACCESS_KEY_ID",
		},
		{
			name: "a named local host accepts the development credentials",
			env: map[string]string{
				"API_S3_ENDPOINT": "http://localhost:4566",
			},
			wantEndpoint:  "http://localhost:4566",
			wantBucket:    "cinema-m3-tickets",
			wantRegion:    "us-east-1",
			wantAccessKey: "floci",
			wantSecret:    "floci",
			wantPresign:   5 * time.Minute,
		},
		{
			name: "a remote endpoint accepts real credentials",
			env: map[string]string{
				"API_S3_ENDPOINT":          "https://objects.example.test",
				"API_S3_ACCESS_KEY_ID":     "AKIAEXAMPLE",
				"API_S3_SECRET_ACCESS_KEY": secret,
			},
			wantEndpoint:  "https://objects.example.test",
			wantBucket:    "cinema-m3-tickets",
			wantRegion:    "us-east-1",
			wantAccessKey: "AKIAEXAMPLE",
			wantSecret:    secret,
			wantPresign:   5 * time.Minute,
		},
		{
			name:      "an unparseable pre-sign lifetime is rejected",
			env:       map[string]string{"API_S3_PRESIGN_TTL": "five minutes"},
			wantError: "API_S3_PRESIGN_TTL",
		},
		{
			name:      "a pre-sign lifetime without a unit is rejected",
			env:       map[string]string{"API_S3_PRESIGN_TTL": "300"},
			wantError: "API_S3_PRESIGN_TTL",
		},
		{
			name:      "a zero pre-sign lifetime is rejected",
			env:       map[string]string{"API_S3_PRESIGN_TTL": "0s"},
			wantError: "API_S3_PRESIGN_TTL",
		},
		{
			name:      "a negative pre-sign lifetime is rejected",
			env:       map[string]string{"API_S3_PRESIGN_TTL": "-1m"},
			wantError: "API_S3_PRESIGN_TTL",
		},
		{
			name:      "a pre-sign lifetime beyond the seven day signature limit is rejected",
			env:       map[string]string{"API_S3_PRESIGN_TTL": "169h"},
			wantError: "API_S3_PRESIGN_TTL",
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
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error message leaked the secret: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.S3Endpoint != tc.wantEndpoint {
				t.Errorf("S3Endpoint = %q, want %q", cfg.S3Endpoint, tc.wantEndpoint)
			}
			if cfg.S3Bucket != tc.wantBucket {
				t.Errorf("S3Bucket = %q, want %q", cfg.S3Bucket, tc.wantBucket)
			}
			if cfg.S3Region != tc.wantRegion {
				t.Errorf("S3Region = %q, want %q", cfg.S3Region, tc.wantRegion)
			}
			if cfg.S3AccessKeyID != tc.wantAccessKey {
				t.Errorf("S3AccessKeyID = %q, want %q", cfg.S3AccessKeyID, tc.wantAccessKey)
			}
			if cfg.S3SecretAccessKey != tc.wantSecret {
				t.Errorf("S3SecretAccessKey does not match the configured secret")
			}
			if cfg.S3PresignTTL != tc.wantPresign {
				t.Errorf("S3PresignTTL = %v, want %v", cfg.S3PresignTTL, tc.wantPresign)
			}
		})
	}
}

// ticketQRKeyMinLength mirrors the documented minimum length of the ticket QR
// signing key, so the test states the number a deployment has to satisfy.
const ticketQRKeyMinLength = 32

// TestTicketQRKeyConfiguration pins the ticket QR signing key rules: a well-known
// development default that works with no variables at all in development, an
// explicit key of at least 32 bytes anywhere, and a refusal everywhere else — a key
// that ships in a repository signs nothing, so the default must never become a
// production key by accident. No case may be satisfied by echoing the key.
func TestTicketQRKeyConfiguration(t *testing.T) {
	const (
		explicitKey = "m3-configured-ticket-qr-key-0123456789"
		shortKey    = "too-short-for-a-ticket-qr-key"
		// The documented development default, spelled out so this test fails if
		// somebody changes it without thinking about the environments.
		developmentDefault = "cinema-m3-development-ticket-qr-key"
	)

	cases := []struct {
		name     string
		env      map[string]string
		wantKey  string
		wantErr  string
		wantWord string
	}{
		{
			name:    "development falls back to the documented default",
			env:     map[string]string{"API_ENV": "development"},
			wantKey: developmentDefault,
		},
		{
			name: "development accepts an explicit key",
			env: map[string]string{
				"API_ENV":           "development",
				"API_TICKET_QR_KEY": explicitKey,
				"API_DATABASE_URL":  testDatabaseURL,
			},
			wantKey: explicitKey,
		},
		{
			name:    "production accepts an explicit key",
			env:     map[string]string{"API_ENV": "production", "API_TICKET_QR_KEY": explicitKey},
			wantKey: explicitKey,
		},
		{
			name:    "a key of exactly the documented minimum is accepted",
			env:     map[string]string{"API_ENV": "production", "API_TICKET_QR_KEY": strings.Repeat("k", ticketQRKeyMinLength)},
			wantKey: strings.Repeat("k", ticketQRKeyMinLength),
		},
		{
			name:     "a key below the documented minimum is refused",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_QR_KEY": shortKey},
			wantErr:  "API_TICKET_QR_KEY",
			wantWord: "32 bytes",
		},
		{
			name:     "a blank key is refused even in development",
			env:      map[string]string{"API_ENV": "development", "API_TICKET_QR_KEY": strings.Repeat(" ", 40)},
			wantErr:  "API_TICKET_QR_KEY",
			wantWord: "blank",
		},
		{
			name:     "the development default is refused outside development",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_QR_KEY": developmentDefault},
			wantErr:  "API_TICKET_QR_KEY",
			wantWord: "development",
		},
		{
			name:     "the development default is refused in an unstated environment",
			env:      map[string]string{"API_TICKET_QR_KEY": developmentDefault},
			wantErr:  "API_TICKET_QR_KEY",
			wantWord: "development",
		},
		{
			// A missing key is left empty here: this configuration is shared by commands
			// that never serve a ticket, and the API process refuses to mount the ticket
			// surface without a usable key (pinned in cmd/api).
			name: "a missing key is left to the process that serves tickets",
			env:  map[string]string{"API_ENV": "production", "API_DATABASE_URL": testDatabaseURL},
		},
		{
			name: "a missing key in an unstated environment is left to the process as well",
			env:  map[string]string{"API_DATABASE_URL": testDatabaseURL},
		},
		{
			// Without a database URL this process cannot reach a ticket, so no surface
			// needs a key and a key would sign nothing; `make up` runs exactly this
			// configuration. That leniency never reaches a route.
			name: "a missing key is tolerated when the process serves no tickets",
			env:  map[string]string{"API_ENV": "production"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(func(key string) string { return testCase.env[key] })
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error naming %s, got nil", testCase.wantErr)
				}
				if !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %q, want it to name %s", err, testCase.wantErr)
				}
				if !strings.Contains(strings.ToLower(err.Error()), testCase.wantWord) {
					t.Fatalf("error = %q, want it to mention %q", err, testCase.wantWord)
				}
				// A wrong key is a secret too: it must never be echoed.
				for _, secret := range []string{shortKey, developmentDefault, strings.Repeat(" ", 40)} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error message leaked the key: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.TicketQRKey != testCase.wantKey {
				t.Fatalf("TicketQRKey = %q, want %q", cfg.TicketQRKey, testCase.wantKey)
			}
			if cfg.TicketQRKey != "" && len(cfg.TicketQRKey) < ticketQRKeyMinLength {
				t.Fatalf("an accepted key is shorter than the documented minimum: %d bytes", len(cfg.TicketQRKey))
			}
		})
	}
}

// A configuration that can serve tickets never holds the development key by accident:
// the accepted development default is only reachable in development, and every other
// environment either supplies its own key or is left to the API process, which refuses
// to mount the ticket surface without one (pinned in cmd/api).
func TestTicketQRKeyNeverShipsTheDevelopmentDefault(t *testing.T) {
	for _, environment := range []string{"", "production", "staging", "test"} {
		env := map[string]string{"API_ENV": environment, "API_DATABASE_URL": testDatabaseURL}
		cfg, err := config.LoadFrom(func(key string) string { return env[key] })
		if err != nil {
			// A refusal is a fine answer; what must never happen is a config that starts
			// with a key published in this repository.
			continue
		}
		if cfg.TicketQRKey != "" {
			t.Fatalf("API_ENV=%q configured TicketQRKey %q without one being supplied", environment, cfg.TicketQRKey)
		}
	}
}

// ticketLinkKeyMinLength mirrors the documented minimum length of the signed ticket link
// key, so the test states the number a deployment has to satisfy.
const ticketLinkKeyMinLength = 32

// TestTicketLinkConfiguration pins the link key and lifetime rules: a development default
// that works with no variables in development, an explicit key of at least 32 bytes
// anywhere, the documented seven day lifetime by default, an explicit lifetime honoured,
// and a refusal for every key and every lifetime that cannot work. The key is never
// echoed by a refusal.
func TestTicketLinkConfiguration(t *testing.T) {
	const (
		explicitKey = "m3-configured-ticket-link-key-0123456789"
		shortKey    = "too-short-for-a-ticket-link-key"
		// The documented development default, spelled out so this test fails if somebody
		// changes it without thinking about the environments.
		developmentDefault = "cinema-m3-development-ticket-link-key"
	)

	cases := []struct {
		name     string
		env      map[string]string
		wantKey  string
		wantTTL  time.Duration
		wantErr  string
		wantWord string
	}{
		{
			name: "development falls back to the documented default",
			env:  map[string]string{"API_ENV": "development"},
			// Seven days: the demo window the study documents, long enough for a booking
			// made a week before the screening.
			wantKey: developmentDefault,
			wantTTL: 168 * time.Hour,
		},
		{
			name: "production accepts an explicit key and leaves the default lifetime",
			env:  map[string]string{"API_ENV": "production", "API_TICKET_LINK_KEY": explicitKey},

			wantKey: explicitKey,
			wantTTL: 168 * time.Hour,
		},
		{
			name: "development accepts an explicit key",
			env:  map[string]string{"API_ENV": "development", "API_TICKET_LINK_KEY": explicitKey},

			wantKey: explicitKey,
			wantTTL: 168 * time.Hour,
		},
		{
			name: "a key of exactly the documented minimum is accepted",
			env:  map[string]string{"API_ENV": "production", "API_TICKET_LINK_KEY": strings.Repeat("k", ticketLinkKeyMinLength)},

			wantKey: strings.Repeat("k", ticketLinkKeyMinLength),
			wantTTL: 168 * time.Hour,
		},
		{
			name: "an explicit lifetime is honoured",
			env:  map[string]string{"API_ENV": "production", "API_TICKET_LINK_TTL": "24h"},

			wantTTL: 24 * time.Hour,
		},
		{
			name: "an explicit lifetime in minutes is honoured",
			env:  map[string]string{"API_ENV": "production", "API_TICKET_LINK_TTL": " 90m "},

			wantTTL: 90 * time.Minute,
		},
		{
			name:     "a key below the documented minimum is refused",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_LINK_KEY": shortKey},
			wantErr:  "API_TICKET_LINK_KEY",
			wantWord: "32 bytes",
		},
		{
			name:     "a blank key is refused even in development",
			env:      map[string]string{"API_ENV": "development", "API_TICKET_LINK_KEY": strings.Repeat(" ", 40)},
			wantErr:  "API_TICKET_LINK_KEY",
			wantWord: "blank",
		},
		{
			name:     "the development default is refused outside development",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_LINK_KEY": developmentDefault},
			wantErr:  "API_TICKET_LINK_KEY",
			wantWord: "development",
		},
		{
			name:     "the development default is refused in an unstated environment",
			env:      map[string]string{"API_TICKET_LINK_KEY": developmentDefault},
			wantErr:  "API_TICKET_LINK_KEY",
			wantWord: "development",
		},
		{
			name:     "a zero lifetime is refused",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_LINK_TTL": "0s"},
			wantErr:  "API_TICKET_LINK_TTL",
			wantWord: "positive",
		},
		{
			name:     "a negative lifetime is refused",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_LINK_TTL": "-1h"},
			wantErr:  "API_TICKET_LINK_TTL",
			wantWord: "positive",
		},
		{
			name:     "an unparsable lifetime is refused",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_LINK_TTL": "seven days"},
			wantErr:  "API_TICKET_LINK_TTL",
			wantWord: "duration",
		},
		{
			name:     "a lifetime without a unit is refused",
			env:      map[string]string{"API_ENV": "production", "API_TICKET_LINK_TTL": "168"},
			wantErr:  "API_TICKET_LINK_TTL",
			wantWord: "duration",
		},
		{
			// A missing key is left empty here, for the reason the QR key is: commands
			// that never sign a link share this configuration, and the API process refuses
			// to mount the link route without a usable key.
			name: "a missing key is left to the process that serves the links",
			env:  map[string]string{"API_ENV": "production"},

			wantTTL: 168 * time.Hour,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(func(key string) string { return testCase.env[key] })
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error naming %s, got nil", testCase.wantErr)
				}
				if !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %q, want it to name %s", err, testCase.wantErr)
				}
				if !strings.Contains(strings.ToLower(err.Error()), testCase.wantWord) {
					t.Fatalf("error = %q, want it to mention %q", err, testCase.wantWord)
				}
				// A wrong key is a secret too, and a lifetime is not worth echoing either.
				for _, secret := range []string{shortKey, developmentDefault, strings.Repeat(" ", 40)} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error message leaked the key: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.TicketLinkKey != testCase.wantKey {
				t.Fatalf("TicketLinkKey = %q, want %q", cfg.TicketLinkKey, testCase.wantKey)
			}
			if cfg.TicketLinkKey != "" && len(cfg.TicketLinkKey) < ticketLinkKeyMinLength {
				t.Fatalf("an accepted key is shorter than the documented minimum: %d bytes", len(cfg.TicketLinkKey))
			}
			if cfg.TicketLinkTTL != testCase.wantTTL {
				t.Fatalf("TicketLinkTTL = %v, want %v", cfg.TicketLinkTTL, testCase.wantTTL)
			}
		})
	}
}

// TestTicketLinkKeyIsNotTheQRKey pins the separation the study asks for: rotating the key
// of an old email's link must never invalidate a printed QR code, nor the other way round,
// and the two development defaults must not be one value.
func TestTicketLinkKeyIsNotTheQRKey(t *testing.T) {
	cfg, err := config.LoadFrom(func(key string) string {
		if key == "API_ENV" {
			return "development"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}
	if cfg.TicketLinkKey == "" || cfg.TicketQRKey == "" {
		t.Fatalf("development defaults = link %q, qr %q; want both", cfg.TicketLinkKey, cfg.TicketQRKey)
	}
	if cfg.TicketLinkKey == cfg.TicketQRKey {
		t.Fatalf("the link and QR development keys are the same value: %q", cfg.TicketLinkKey)
	}
}

// TestS3ConfigurationNeverEchoesTheSecret pins that a rejected object-storage
// configuration names the variables only: the secret access key must never reach
// a log or a crash report through an error message.
func TestS3ConfigurationNeverEchoesTheSecret(t *testing.T) {
	const secret = "broken-bucket-secret-value"
	env := map[string]string{
		"API_S3_BUCKET":            "Not_A_Bucket",
		"API_S3_ACCESS_KEY_ID":     "AKIAEXAMPLE",
		"API_S3_SECRET_ACCESS_KEY": secret,
	}
	_, err := config.LoadFrom(func(key string) string { return env[key] })
	if err == nil {
		t.Fatal("expected an error for an invalid bucket, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaked the secret: %v", err)
	}
}

// TestEmailConfiguration pins the boundary S5 draws for the ticket email: no provider is
// the normal state, in which the consumer is not built and the gates stay green; half a
// provider is refused at startup and the refusal names the missing key, because a
// half-configured provider would fail every pass identically; and the public origin the
// link is built from is validated so a link can never be minted that opens nothing.
func TestEmailConfiguration(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		wantEnabled bool
		wantAddr    string
		wantFrom    string
		wantWeb     string
		wantErr     string
		wantWord    string
	}{
		{
			name:    "no provider is the normal state and leaves the ticket email disabled",
			env:     map[string]string{},
			wantWeb: "http://localhost:3000",
		},
		{
			name:        "both settings enable it and keep their values",
			env:         map[string]string{"API_SMTP_ADDR": "mailpit:1025", "API_EMAIL_FROM": "tickets@cinema.test"},
			wantEnabled: true,
			wantAddr:    "mailpit:1025",
			wantFrom:    "tickets@cinema.test",
			wantWeb:     "http://localhost:3000",
		},
		{
			name:        "the settings are trimmed like the rest of the configuration",
			env:         map[string]string{"API_SMTP_ADDR": " 127.0.0.1:1025 ", "API_EMAIL_FROM": " tickets@cinema.test "},
			wantEnabled: true,
			wantAddr:    "127.0.0.1:1025",
			wantFrom:    "tickets@cinema.test",
			wantWeb:     "http://localhost:3000",
		},
		{
			name:     "an address without a sender is refused by the name of the missing key",
			env:      map[string]string{"API_SMTP_ADDR": "mailpit:1025"},
			wantErr:  "API_EMAIL_FROM",
			wantWord: "together",
		},
		{
			name:     "a sender without an address is refused by the name of the missing key",
			env:      map[string]string{"API_EMAIL_FROM": "tickets@cinema.test"},
			wantErr:  "API_SMTP_ADDR",
			wantWord: "together",
		},
		{
			name:    "a supplied public origin is kept",
			env:     map[string]string{"API_PUBLIC_WEB_URL": "https://cinema.test"},
			wantWeb: "https://cinema.test",
		},
		{
			name:    "the trailing slash of a supplied origin is trimmed",
			env:     map[string]string{"API_PUBLIC_WEB_URL": "https://cinema.test/"},
			wantWeb: "https://cinema.test",
		},
		{
			// A deployment served under a path prefix keeps it: the link route is appended
			// to whatever origin the web is served from, so refusing a path here would
			// refuse an origin that works.
			name:    "an origin served under a path keeps it and loses its trailing slash",
			env:     map[string]string{"API_PUBLIC_WEB_URL": "https://cinema.test/ticketing/"},
			wantWeb: "https://cinema.test/ticketing",
		},
		{
			name:     "a relative origin is refused",
			env:      map[string]string{"API_PUBLIC_WEB_URL": "/tickets"},
			wantErr:  "API_PUBLIC_WEB_URL",
			wantWord: "absolute",
		},
		{
			name:     "an origin without a scheme is refused",
			env:      map[string]string{"API_PUBLIC_WEB_URL": "cinema.test"},
			wantErr:  "API_PUBLIC_WEB_URL",
			wantWord: "absolute",
		},
		{
			name:     "a scheme that a browser cannot open is refused",
			env:      map[string]string{"API_PUBLIC_WEB_URL": "ftp://cinema.test"},
			wantErr:  "API_PUBLIC_WEB_URL",
			wantWord: "http",
		},
		{
			name:     "an origin without a host is refused",
			env:      map[string]string{"API_PUBLIC_WEB_URL": "http:///tickets"},
			wantErr:  "API_PUBLIC_WEB_URL",
			wantWord: "host",
		},
		{
			name:     "an origin carrying a query is refused",
			env:      map[string]string{"API_PUBLIC_WEB_URL": "https://cinema.test/?campaign=mail"},
			wantErr:  "API_PUBLIC_WEB_URL",
			wantWord: "query",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(func(key string) string { return testCase.env[key] })
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error naming %s, got nil", testCase.wantErr)
				}
				if !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %q, want it to name %s", err, testCase.wantErr)
				}
				if !strings.Contains(strings.ToLower(err.Error()), testCase.wantWord) {
					t.Fatalf("error = %q, want it to mention %q", err, testCase.wantWord)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.EmailEnabled != testCase.wantEnabled {
				t.Fatalf("EmailEnabled = %t, want %t", cfg.EmailEnabled, testCase.wantEnabled)
			}
			if cfg.SMTPAddr != testCase.wantAddr {
				t.Fatalf("SMTPAddr = %q, want %q", cfg.SMTPAddr, testCase.wantAddr)
			}
			if cfg.EmailFrom != testCase.wantFrom {
				t.Fatalf("EmailFrom = %q, want %q", cfg.EmailFrom, testCase.wantFrom)
			}
			if cfg.PublicWebURL != testCase.wantWeb {
				t.Fatalf("PublicWebURL = %q, want %q", cfg.PublicWebURL, testCase.wantWeb)
			}
			if strings.HasSuffix(cfg.PublicWebURL, "/") {
				t.Fatalf("PublicWebURL = %q, want no trailing slash: the link route appends its own", cfg.PublicWebURL)
			}
		})
	}
}

// identityLinkKeyMinLength mirrors the documented minimum length of the signed access link
// key, so the test states the number a deployment has to satisfy.
const identityLinkKeyMinLength = 32

// TestIdentityLinkConfiguration pins the signed access link rules (T2, S2): a development
// default that works with no variables in development, an explicit key of at least 32 bytes
// anywhere, the documented fifteen minute lifetime by default — an access link is a step,
// not a key, so it lives in minutes and not in days — an explicit lifetime honoured, and a
// refusal for every key and every lifetime that cannot work. No case may be satisfied by
// echoing the key.
func TestIdentityLinkConfiguration(t *testing.T) {
	const (
		explicitKey = "m1-configured-identity-link-key-0123456789"
		shortKey    = "too-short-for-an-access-link"
		// The documented development default, spelled out so this test fails if somebody
		// changes it without thinking about the environments.
		developmentDefault = "cinema-identity-development-access-link-key"
	)

	cases := []struct {
		name     string
		env      map[string]string
		wantKey  string
		wantTTL  time.Duration
		wantErr  string
		wantWord string
	}{
		{
			name:    "development falls back to the documented default",
			env:     map[string]string{"API_ENV": "development"},
			wantKey: developmentDefault,
			// Fifteen minutes: an access link is one step of a login, not a key somebody
			// keeps, so a longer window would be a longer-lived way into an account.
			wantTTL: 15 * time.Minute,
		},
		{
			name:    "production accepts an explicit key and leaves the default lifetime",
			env:     map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_KEY": explicitKey},
			wantKey: explicitKey,
			wantTTL: 15 * time.Minute,
		},
		{
			name:    "development accepts an explicit key",
			env:     map[string]string{"API_ENV": "development", "API_IDENTITY_LINK_KEY": explicitKey},
			wantKey: explicitKey,
			wantTTL: 15 * time.Minute,
		},
		{
			name:    "a key of exactly the documented minimum is accepted",
			env:     map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_KEY": strings.Repeat("k", identityLinkKeyMinLength)},
			wantKey: strings.Repeat("k", identityLinkKeyMinLength),
			wantTTL: 15 * time.Minute,
		},
		{
			name:    "an explicit lifetime is honoured",
			env:     map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_TTL": "30m"},
			wantTTL: 30 * time.Minute,
		},
		{
			name:    "an explicit lifetime in seconds is honoured",
			env:     map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_TTL": " 90s "},
			wantTTL: 90 * time.Second,
		},
		{
			name:     "a key below the documented minimum is refused",
			env:      map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_KEY": shortKey},
			wantErr:  "API_IDENTITY_LINK_KEY",
			wantWord: "32 bytes",
		},
		{
			name:     "a blank key is refused even in development",
			env:      map[string]string{"API_ENV": "development", "API_IDENTITY_LINK_KEY": strings.Repeat(" ", 40)},
			wantErr:  "API_IDENTITY_LINK_KEY",
			wantWord: "blank",
		},
		{
			name:     "the development default is refused outside development",
			env:      map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_KEY": developmentDefault},
			wantErr:  "API_IDENTITY_LINK_KEY",
			wantWord: "development",
		},
		{
			name:     "the development default is refused in an unstated environment",
			env:      map[string]string{"API_IDENTITY_LINK_KEY": developmentDefault},
			wantErr:  "API_IDENTITY_LINK_KEY",
			wantWord: "development",
		},
		{
			name:     "a zero lifetime is refused",
			env:      map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_TTL": "0s"},
			wantErr:  "API_IDENTITY_LINK_TTL",
			wantWord: "positive",
		},
		{
			name:     "a negative lifetime is refused",
			env:      map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_TTL": "-1m"},
			wantErr:  "API_IDENTITY_LINK_TTL",
			wantWord: "positive",
		},
		{
			name:     "an unparsable lifetime is refused",
			env:      map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_TTL": "fifteen minutes"},
			wantErr:  "API_IDENTITY_LINK_TTL",
			wantWord: "duration",
		},
		{
			name:     "a lifetime without a unit is refused",
			env:      map[string]string{"API_ENV": "production", "API_IDENTITY_LINK_TTL": "15"},
			wantErr:  "API_IDENTITY_LINK_TTL",
			wantWord: "duration",
		},
		{
			// A missing key is left empty here, for the reason the other two keys are: this
			// configuration is shared by commands that never sign a link, and the API
			// process refuses to mount the identity surface without a usable key.
			name:    "a missing key is left to the process that serves the links",
			env:     map[string]string{"API_ENV": "production"},
			wantKey: "",
			wantTTL: 15 * time.Minute,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg, err := config.LoadFrom(func(key string) string { return testCase.env[key] })
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error naming %s, got nil", testCase.wantErr)
				}
				if !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %q, want it to name %s", err, testCase.wantErr)
				}
				if !strings.Contains(strings.ToLower(err.Error()), testCase.wantWord) {
					t.Fatalf("error = %q, want it to mention %q", err, testCase.wantWord)
				}
				// A wrong key is a secret too, and a lifetime is not worth echoing either.
				for _, secret := range []string{shortKey, developmentDefault, strings.Repeat(" ", 40)} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error message leaked the key: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom returned error: %v", err)
			}
			if cfg.IdentityLinkKey != testCase.wantKey {
				t.Fatalf("IdentityLinkKey = %q, want %q", cfg.IdentityLinkKey, testCase.wantKey)
			}
			if cfg.IdentityLinkKey != "" && len(cfg.IdentityLinkKey) < identityLinkKeyMinLength {
				t.Fatalf("an accepted key is shorter than the documented minimum: %d bytes", len(cfg.IdentityLinkKey))
			}
			if cfg.IdentityLinkTTL != testCase.wantTTL {
				t.Fatalf("IdentityLinkTTL = %v, want %v", cfg.IdentityLinkTTL, testCase.wantTTL)
			}
		})
	}
}

// TestTheThreeDevelopmentLinkKeysAreDistinct pins the separation the first two keys already
// had, extended to the access link: a QR code is printed on paper, a ticket link lives in a
// mailbox and an access link proves a mailbox, so rotating or leaking one must never touch
// the others. The three development defaults must therefore be three values, and sharing one
// by accident has to fail here rather than in a deployment.
func TestTheThreeDevelopmentLinkKeysAreDistinct(t *testing.T) {
	cfg, err := config.LoadFrom(func(key string) string {
		if key == "API_ENV" {
			return "development"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}

	keys := []struct {
		name  string
		value string
	}{
		{"API_TICKET_QR_KEY", cfg.TicketQRKey},
		{"API_TICKET_LINK_KEY", cfg.TicketLinkKey},
		{"API_IDENTITY_LINK_KEY", cfg.IdentityLinkKey},
	}
	for _, key := range keys {
		if key.value == "" {
			t.Fatalf("%s = empty in development, want the documented default", key.name)
		}
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[i].value == keys[j].value {
				t.Fatalf("%s and %s hold the same development key %q, want three distinct values",
					keys[i].name, keys[j].name, keys[i].value)
			}
		}
	}
}
