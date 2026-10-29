package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
)

const adminTestURL = "postgres://cinema:s3cr3t-pw@127.0.0.1:5432/cinema?sslmode=disable"

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(discard{}, nil)) }

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func testServer() *httpserver.Server {
	return httpserver.New(config.Config{}, health.New(health.ProcessProbe{}), discardLogger())
}

// adminRouted reports whether the admin route is reachable without a guard
// denial, i.e. whether anything was mounted under /api/v1/admin/.
func adminRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/cinemas", nil))
	return w.Code != http.StatusNotFound
}

// S7 — with local administration disabled the runnable API mounts nothing, so
// the M0 configuration keeps its routes and lifetime unchanged.
func TestMountAdminDisabledMountsNothing(t *testing.T) {
	srv := testServer()
	store, err := mountAdmin(config.Config{}, srv, discardLogger())
	if err != nil {
		t.Fatalf("mountAdmin returned error: %v", err)
	}
	if store != nil {
		t.Fatalf("mountAdmin returned a store, want nil")
	}
	if adminRouted(srv) {
		t.Fatal("admin route is reachable with local administration disabled")
	}

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("health live status = %d, want 200", w.Code)
	}
}

// A production configuration never mounts the administrative subtree, even when
// the enabled flag is somehow set.
func TestMountAdminProductionMountsNothing(t *testing.T) {
	cfg := config.Config{Environment: "production", LocalAdminEnabled: false, LocalAdminToken: "token", DatabaseURL: adminTestURL}
	srv := testServer()
	store, err := mountAdmin(cfg, srv, discardLogger())
	if err != nil {
		t.Fatalf("mountAdmin returned error: %v", err)
	}
	if store != nil {
		t.Fatalf("mountAdmin returned a store, want nil")
	}
	if adminRouted(srv) {
		t.Fatal("admin route is reachable in a production configuration")
	}
}

// An enabled configuration without a database URL is refused instead of opening
// a pool from ambient defaults, and the refusal never echoes secret material.
func TestMountAdminRequiresDatabaseURL(t *testing.T) {
	cfg := config.Config{Environment: "development", LocalAdminEnabled: true, LocalAdminToken: "token"}
	srv := testServer()
	store, err := mountAdmin(cfg, srv, discardLogger())
	if err == nil {
		t.Fatal("mountAdmin succeeded without a database URL, want an error")
	}
	if store != nil {
		t.Fatalf("mountAdmin returned a store, want nil")
	}
	if adminRouted(srv) {
		t.Fatal("admin route is reachable without a database URL")
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("error leaked configuration secrets: %v", err)
	}
}

// The driver quotes the connection string in parse errors, so diagnostics are
// redacted before they reach a log while keeping the driver's reason.
func TestRedactDatabaseURL(t *testing.T) {
	cases := []struct {
		name    string
		message string
		reason  string
	}{
		{
			name:    "driver quotes the full connection string",
			message: "cannot parse `" + adminTestURL + "`: failed to parse as URL",
			reason:  "failed to parse as URL",
		},
		{
			name:    "driver masks the password but keeps the connection string",
			message: "cannot parse `postgres://cinema:xxxxxx@%zz/cinema`: invalid URL escape",
			reason:  "invalid URL escape",
		},
		{
			name:    "driver reports the password alone",
			message: "cannot parse password s3cr3t-pw: malformed userinfo",
			reason:  "malformed userinfo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDatabaseURL(tc.message, adminTestURL)
			for _, leaked := range []string{"s3cr3t-pw", "xxxxxx", "postgres://"} {
				if strings.Contains(got, leaked) {
					t.Fatalf("redaction leaked %q: %q", leaked, got)
				}
			}
			if !strings.Contains(got, tc.reason) {
				t.Fatalf("redaction dropped the driver reason %q: %q", tc.reason, got)
			}
		})
	}

	if got := redactDatabaseURL("cannot parse `x`", ""); got != "cannot parse `x`" {
		t.Fatalf("empty database URL changed the message: %q", got)
	}
}
