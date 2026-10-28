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
func LoadFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:             ":8080",
		ShutdownTimeout:  15 * time.Second,
		ReadinessTimeout: 5 * time.Second,
		LogLevel:         "info",
		Dependencies:     []string{},
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

	return cfg, nil
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
