package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/reservation"
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

// reservationRouted reports whether the public reservation subtree is mounted,
// i.e. whether a hold request reaches the handler instead of a 404. An empty body
// is rejected by the transport before any I/O, so this probe needs no database.
func reservationRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/reservations/holds", strings.NewReader("")))
	return w.Code != http.StatusNotFound
}

// S7: the default M0/M1 configuration has no database URL, so the reservation
// surface stays unmounted and the existing routes and lifetime are unchanged.
func TestMountReservationDisabledMountsNothing(t *testing.T) {
	srv := testServer()
	proc, err := mountReservation(config.Config{}, srv, discardLogger())
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc != nil {
		proc.Close()
		t.Fatalf("mountReservation returned a process, want nil without a database URL")
	}
	if reservationRouted(srv) {
		t.Fatal("the reservation surface is mounted without a database URL")
	}

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("health live status = %d, want 200", w.Code)
	}
}

// S7: with a database URL the public reservation surface is mounted at /api/v1
// outside the administrative guard, and the expiry sweeper is built from the
// configured cadence and batch.
func TestMountReservationMountsThePublicSurface(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	}
	srv := testServer()
	proc, err := mountReservationWithProbe(cfg, srv, discardLogger(), presentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()
	if proc.sweeper == nil {
		t.Fatal("mountReservation built no expiry sweeper")
	}
	if !reservationRouted(srv) {
		t.Fatal("the public reservation surface was not mounted")
	}
	if adminRouted(srv) {
		t.Fatal("mounting the public surface exposed the administrative subtree")
	}
}

// A reservation configuration that cannot be used is refused, and the refusal
// never echoes the database URL or its password.
func TestMountReservationRejectsUnusableConfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
	}{
		{"unparseable database URL", config.Config{DatabaseURL: "not-a-url", RedisURL: "redis://127.0.0.1:6379/0", HoldTTL: time.Minute, ExpiryInterval: time.Second, ExpiryBatch: 10}},
		{"unparseable redis URL", config.Config{DatabaseURL: adminTestURL, RedisURL: "://nope", HoldTTL: time.Minute, ExpiryInterval: time.Second, ExpiryBatch: 10}},
		{"zero hold window", config.Config{DatabaseURL: adminTestURL, RedisURL: "redis://127.0.0.1:6379/0", HoldTTL: 0, ExpiryInterval: time.Second, ExpiryBatch: 10}},
		{"zero expiry cadence", config.Config{DatabaseURL: adminTestURL, RedisURL: "redis://127.0.0.1:6379/0", HoldTTL: time.Minute, ExpiryInterval: 0, ExpiryBatch: 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := testServer()
			proc, err := mountReservationWithProbe(tc.cfg, srv, discardLogger(), presentSchema)
			if err == nil {
				proc.Close()
				t.Fatal("mountReservation succeeded, want a refusal")
			}
			if proc != nil {
				proc.Close()
				t.Fatal("a refused configuration returned a process")
			}
			if reservationRouted(srv) {
				t.Fatal("a refused configuration still mounted the reservation surface")
			}
			for _, leaked := range []string{"s3cr3t-pw", "postgres://"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error leaked %q: %v", leaked, err)
				}
			}
		})
	}
}

// presentSchema and absentSchema stand in for the database check, so the mount
// decision is testable without a database.
func presentSchema(context.Context, *reservation.PostgresStore) (bool, error) { return true, nil }
func absentSchema(context.Context, *reservation.PostgresStore) (bool, error)  { return false, nil }
func failingSchema(context.Context, *reservation.PostgresStore) (bool, error) {
	return false, errors.New("database unreachable")
}

// An older schema must disable the whole reservation surface: no public routes and
// no expiry worker, with one informative line instead of an error on every tick.
func TestMountReservationSkipsAnOlderSchema(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	}
	srv := testServer()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	proc, err := mountReservationWithProbe(cfg, srv, logger, absentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc != nil {
		t.Fatalf("mountReservation returned a process without the reservation schema")
	}
	if reservationRouted(srv) {
		t.Fatal("the public reservation surface was mounted without its schema")
	}
	if !strings.Contains(logged.String(), "reservation surface disabled") {
		t.Fatalf("log = %q, want the one informative line about the missing schema", logged.String())
	}
	if strings.Contains(logged.String(), "expiry sweep failed") {
		t.Fatal("the worker must not run against a schema that lacks the reservation tables")
	}
}

// A probe that cannot answer is a startup failure, not a silent skip: mounting a
// surface whose schema state is unknown would be guessing.
func TestMountReservationFailsWhenTheProbeCannotAnswer(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	}
	srv := testServer()
	proc, err := mountReservationWithProbe(cfg, srv, discardLogger(), failingSchema)
	if err == nil {
		t.Fatal("mountReservation returned no error for an unanswerable schema probe")
	}
	if proc != nil {
		t.Fatal("mountReservation returned a process for an unanswerable schema probe")
	}
	if reservationRouted(srv) {
		t.Fatal("the public surface was mounted although the schema state is unknown")
	}
}
