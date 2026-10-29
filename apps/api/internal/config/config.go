// Package config loads the API configuration from environment variables.
//
// M0 foundation only. It exposes a minimal, dependency-free configuration
// surface consumed by cmd/api; it performs no I/O beyond reading the process
// environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime configuration for the API process.
type Config struct {
	// Addr is the host:port the HTTP server listens on.
	Addr string
	// ShutdownTimeout bounds graceful shutdown of in-flight requests.
	ShutdownTimeout time.Duration
	// ReadinessTimeout bounds a single readiness probe evaluation.
	ReadinessTimeout time.Duration
	// LogLevel selects the structured logging verbosity.
	LogLevel string
	// Dependencies names the optional dependency probes wired into readiness.
	Dependencies []string
	// LocalAdminEnabled is explicitly enabled only for development.
	LocalAdminEnabled bool
	// Environment controls whether local administration may be enabled.
	Environment string
	// LocalAdminToken is a secret and must never be logged.
	LocalAdminToken string
	// DatabaseURL is required only when local administration is enabled. It may
	// carry credentials, so it must never be logged.
	DatabaseURL string
	// HoldTTL is the reservation hold window. It defaults to ten minutes and
	// accepts any Go duration so tests can use seconds, but it must be positive.
	HoldTTL time.Duration
}

// Load reads configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration using getenv, enabling deterministic tests.
//
// Recognized variables:
//
//	API_ADDR                   listen address (default ":8080")
//	API_LOG_LEVEL              log level debug|info|warn|error (default "info")
//	API_SHUTDOWN_TIMEOUT_MS    graceful shutdown budget in milliseconds (default 15000)
//	API_READINESS_TIMEOUT_MS   readiness deadline in milliseconds (default 5000)
//	API_DEPENDENCIES           comma-separated dependency probe names (default none)
//	API_ENV                    environment; local administration requires development
//	API_LOCAL_ADMIN            true or false; defaults to false
//	API_LOCAL_ADMIN_TOKEN      secret required when local administration is enabled
//	API_DATABASE_URL           database URL; required when local administration is enabled
//	API_HOLD_TTL              reservation hold window as a duration (default "10m"; "30s" is valid)
func LoadFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:             ":8080",
		ShutdownTimeout:  15 * time.Second,
		ReadinessTimeout: 5 * time.Second,
		LogLevel:         "info",
		Dependencies:     []string{},
		HoldTTL:          10 * time.Minute,
	}

	cfg.Environment = getenv("API_ENV")
	cfg.LocalAdminToken = getenv("API_LOCAL_ADMIN_TOKEN")
	cfg.DatabaseURL = strings.TrimSpace(getenv("API_DATABASE_URL"))
	if v := getenv("API_LOCAL_ADMIN"); v != "" {
		if v != "true" && v != "false" {
			return Config{}, fmt.Errorf("API_LOCAL_ADMIN must be true or false")
		}
		cfg.LocalAdminEnabled = v == "true"
	}
	if cfg.LocalAdminEnabled && (cfg.Environment != "development" || cfg.LocalAdminToken == "" || cfg.DatabaseURL == "") {
		// The message names the required settings but never echoes their values,
		// because the token and the database URL may both carry secrets.
		return Config{}, fmt.Errorf("local administration requires development, a nonempty token, and a database URL")
	}

	if v := getenv("API_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := getenv("API_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}

	if v := getenv("API_SHUTDOWN_TIMEOUT_MS"); v != "" {
		d, err := parseMillis(v, "API_SHUTDOWN_TIMEOUT_MS")
		if err != nil {
			return Config{}, err
		}
		cfg.ShutdownTimeout = d
	}
	if v := getenv("API_READINESS_TIMEOUT_MS"); v != "" {
		d, err := parseMillis(v, "API_READINESS_TIMEOUT_MS")
		if err != nil {
			return Config{}, err
		}
		cfg.ReadinessTimeout = d
	}

	if v := getenv("API_DEPENDENCIES"); v != "" {
		cfg.Dependencies = parseDependencies(v)
	}

	if v := getenv("API_HOLD_TTL"); v != "" {
		d, err := parseHoldTTL(v)
		if err != nil {
			return Config{}, err
		}
		cfg.HoldTTL = d
	}

	return cfg, nil
}

// parseHoldTTL accepts a Go duration such as "10m" or, for tests, "30s".
// A value without a unit or a non-positive window is rejected rather than
// silently treated as a zero-length hold.
func parseHoldTTL(v string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("API_HOLD_TTL must be a duration such as 10m or 30s: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("API_HOLD_TTL must be positive, got %q", v)
	}
	return d, nil
}

func parseMillis(v, key string) (time.Duration, error) {
	ms, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if ms < 0 {
		return 0, fmt.Errorf("%s: %d is negative", key, ms)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func parseDependencies(v string) []string {
	deps := []string{}
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			deps = append(deps, p)
		}
	}
	return deps
}
