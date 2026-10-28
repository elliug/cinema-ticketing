package config_test

import (
	"slices"
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
