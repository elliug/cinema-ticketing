package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/payment"
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema)
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
			proc, err := mountReservationWithProbes(tc.cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema)
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

// presentSchema, absentSchema and failingSchema stand in for the reservation
// schema check, so the mount decision is testable without a database.
func presentSchema(context.Context, *reservation.PostgresStore) (bool, error) { return true, nil }
func absentSchema(context.Context, *reservation.PostgresStore) (bool, error)  { return false, nil }
func failingSchema(context.Context, *reservation.PostgresStore) (bool, error) {
	return false, errors.New("database unreachable")
}

// The checkout schema probe is injected the same way: the payment route is only
// mounted when the database carries the M3 objects.
func checkoutPresentSchema(context.Context, *checkout.PostgresStore) (bool, error) {
	return true, nil
}
func checkoutAbsentSchema(context.Context, *checkout.PostgresStore) (bool, error) {
	return false, nil
}
func checkoutFailingSchema(context.Context, *checkout.PostgresStore) (bool, error) {
	return false, errors.New("database unreachable")
}

// paymentRouted reports whether the checkout route is mounted, i.e. whether a
// payment request reaches the handler instead of a 404. The probe sends no
// Idempotency-Key, which the service rejects before any database work, so it needs
// no database.
func paymentRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/v1/reservations/00000000-0000-0000-0000-00000000c0f1/payment", nil))
	return w.Code != http.StatusNotFound
}

// T3 (S7): the checkout route is part of the customer surface, so it is mounted at
// /api/v1 next to the hold routes and outside the administrative guard.
func TestMountReservationMountsTheCheckoutRoute(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()
	if proc.checkoutStore == nil {
		t.Fatal("mountReservation built no checkout store next to the reservation store")
	}
	if !paymentRouted(srv) {
		t.Fatal("the checkout route was not mounted although the checkout schema is present")
	}
	if adminRouted(srv) {
		t.Fatal("mounting the checkout route exposed the administrative subtree")
	}
}

// T3 (S7): the checkout route stays closed against a database without the checkout
// schema — the reservation surface keeps working, one informative line replaces an
// error per request, and no payment route is advertised.
func TestMountReservationSkipsTheCheckoutRouteWithoutItsSchema(t *testing.T) {
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutAbsentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring kept")
	}
	defer proc.Close()
	if proc.checkoutStore != nil {
		t.Fatal("mountReservation kept a checkout store without the checkout schema")
	}
	if paymentRouted(srv) {
		t.Fatal("the checkout route was mounted without the checkout schema")
	}
	if !reservationRouted(srv) {
		t.Fatal("the reservation surface disappeared because the checkout schema is missing")
	}
	if !strings.Contains(logged.String(), "checkout schema not present") {
		t.Fatalf("log = %q, want the one informative line about the missing checkout schema", logged.String())
	}
}

// A checkout schema probe that cannot answer is a startup failure: mounting a
// payment route whose schema state is unknown would be guessing.
func TestMountReservationFailsWhenTheCheckoutProbeCannotAnswer(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutFailingSchema)
	if err == nil {
		t.Fatal("mountReservation returned no error for an unanswerable checkout schema probe")
	}
	if proc != nil {
		t.Fatal("mountReservation returned a process for an unanswerable checkout schema probe")
	}
	if paymentRouted(srv) || reservationRouted(srv) {
		t.Fatal("a surface was mounted although the checkout schema state is unknown")
	}
}

// S8/T3: the provider the checkout is wired with never declines. A decline is a
// test scenario built by constructing an adapter with a decline rule, never the
// default the process runs with, so a real 12,13 EUR ticket is a normal purchase.
func TestWiredPaymentProviderNeverDeclines(t *testing.T) {
	gateway := newPaymentGateway()
	for _, amount := range []int64{13, 1213, 99999913} {
		result, err := gateway.Charge(context.Background(), payment.Request{
			AmountMinorUnits: amount,
			Reference:        fmt.Sprintf("wired-%d", amount),
		})
		if err != nil {
			t.Fatalf("Charge(%d) = unexpected error %v", amount, err)
		}
		if result.Outcome != payment.OutcomeApproved {
			t.Fatalf("Charge(%d) outcome = %q, want %q: the wired provider never declines",
				amount, result.Outcome, payment.OutcomeApproved)
		}
	}
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, absentSchema, checkoutPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), failingSchema, checkoutPresentSchema)
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
