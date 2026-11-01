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

	"cinema.local/ticketing/apps/api/internal/adminapi"
	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/email"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/identity"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/reservation"
	"cinema.local/ticketing/apps/api/internal/tickets"
	"cinema.local/ticketing/apps/api/internal/venue"
)

const adminTestURL = "postgres://cinema:s3cr3t-pw@127.0.0.1:5432/cinema?sslmode=disable"

// testTicketQRKey is the throwaway signing key the mount tests configure: long
// enough for the documented minimum, and never the development default.
const testTicketQRKey = "mount-test-ticket-qr-key-0123456789abcdef"

// testIdentityLinkKey is the throwaway access link key these mount tests configure: long
// enough for the documented minimum, and never one of the other two keys.
const testIdentityLinkKey = "mount-test-identity-link-key-0123456789abcdef"

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
		// The ticket surface this process can serve needs its signing key.
		TicketQRKey: testTicketQRKey,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
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
			proc, err := mountReservationWithProbes(tc.cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
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
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
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
		// The ticket schema is present here, so the ticket routes still need the key.
		TicketQRKey: testTicketQRKey,
	}
	srv := testServer()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutAbsentSchema, ticketPresentSchema, cataloguePresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutFailingSchema, ticketPresentSchema, cataloguePresentSchema)
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

// The ticket detail schema probe is injected the same way: the ticket route is only
// mounted when the database carries the objects its read touches.
func ticketPresentSchema(context.Context, *tickets.PostgresStore) (bool, error) { return true, nil }
func ticketAbsentSchema(context.Context, *tickets.PostgresStore) (bool, error) {
	return false, nil
}
func ticketFailingSchema(context.Context, *tickets.PostgresStore) (bool, error) {
	return false, errors.New("database unreachable")
}

// ticketRouted reports whether the ticket detail route is mounted, i.e. whether a
// ticket request reaches the handler instead of a 404. A mounted route cannot answer
// 404 for a ticket without asking its store, and that store here points at a database
// that does not exist, so the probe never needs a live one.
func ticketRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/tickets/00000000-0000-0000-0000-00000000c0f1", nil))
	return w.Code != http.StatusNotFound
}

// ticketEmailRouted reports whether the ticket page's resend route is mounted. The probe
// presents no body at all, so a mounted route answers its own 400 without touching a database;
// an unmounted one is refused by the mux.
func ticketEmailRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/v1/tickets/00000000-0000-0000-0000-00000000c0f1/email", nil))
	return w.Code != http.StatusNotFound
}

// redeemRouted reports whether the door's admission route is mounted. The probe presents
// no code at all, so a mounted route answers the malformed-body refusal without touching
// a database; an unmounted one is refused by the ticket detail route's path instead
// (405, method not allowed).
func redeemRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/tickets/redeem", nil))
	return w.Code == http.StatusBadRequest
}

// T4 (S4): the ticket detail route is part of the customer surface, so it is mounted
// at /api/v1 next to the hold and payment routes and outside the administrative
// guard. It is the route the confirmation page and the PDF download both address.
func TestMountReservationMountsTheTicketRoute(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	var logged bytes.Buffer
	proc, err := mountReservationWithProbes(cfg, srv, slog.New(slog.NewTextHandler(&logged, nil)),
		presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()
	if proc.ticketStore == nil {
		t.Fatal("mountReservation built no ticket store next to the checkout store")
	}
	if !ticketRouted(srv) {
		t.Fatal("the ticket detail route was not mounted although its schema is present")
	}
	// T7 (S3): the admission route is mounted with the code it shares with the detail
	// route, and the operator's line names it.
	if !redeemRouted(srv) {
		t.Fatal("the ticket admission route was not mounted although its schema is present")
	}
	if !strings.Contains(logged.String(), "/api/v1/tickets/redeem") {
		t.Fatalf("log = %q, want the mounted-routes line to name the admission route", logged.String())
	}
	if !paymentRouted(srv) || !reservationRouted(srv) {
		t.Fatal("mounting the ticket route dropped a route next to it")
	}
	if adminRouted(srv) {
		t.Fatal("mounting the ticket route exposed the administrative subtree")
	}
}

// T4 (S4): a deployment without the ticket schema has issued no tickets, so it
// advertises no ticket route: the rest of the customer surface keeps working, one
// informative line replaces an error per request, and nothing claims a ticket that
// cannot exist there.
func TestMountReservationSkipsTheTicketRouteWithoutItsSchema(t *testing.T) {
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema, ticketAbsentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring kept")
	}
	defer proc.Close()
	if proc.ticketStore != nil {
		t.Fatal("mountReservation kept a ticket store without the ticket schema")
	}
	if ticketRouted(srv) || ticketQRRouted(srv) || redeemRouted(srv) {
		t.Fatal("the ticket routes were mounted without their schema")
	}
	if !reservationRouted(srv) || !paymentRouted(srv) {
		t.Fatal("the reservation and payment routes disappeared because the ticket schema is missing")
	}
	if !strings.Contains(logged.String(), "ticket schema not present") {
		t.Fatalf("log = %q, want the one informative line about the missing ticket schema", logged.String())
	}
}

// A ticket schema probe that cannot answer is a startup failure: mounting a route
// whose schema state is unknown would be guessing.
func TestMountReservationFailsWhenTheTicketProbeCannotAnswer(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketFailingSchema, cataloguePresentSchema)
	if err == nil {
		t.Fatal("mountReservation returned no error for an unanswerable ticket schema probe")
	}
	if proc != nil {
		t.Fatal("mountReservation returned a process for an unanswerable ticket schema probe")
	}
	if ticketRouted(srv) || ticketQRRouted(srv) || paymentRouted(srv) || reservationRouted(srv) {
		t.Fatal("a surface was mounted although the ticket schema state is unknown")
	}
	for _, leaked := range []string{"s3cr3t-pw", "postgres://"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error leaked %q: %v", leaked, err)
		}
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, absentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), failingSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
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

// ticketVerificationRouted reports whether the ticket verification route is mounted,
// i.e. whether a scan reaches the handler instead of a 404. The probe sends an empty
// body, which the transport rejects before any read, so it needs no database.
func ticketVerificationRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/tickets/verify", strings.NewReader("")))
	return w.Code != http.StatusNotFound
}

// ticketQRRouted reports whether the ticket QR image route is mounted, i.e. whether a
// request for the image reaches the handler instead of the mux's 404. A mounted route
// cannot answer 404 for a ticket without asking its store, and that store here points
// at a database that does not exist, so the probe needs no live one.
func ticketQRRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/tickets/00000000-0000-0000-0000-00000000c0f1/qr.svg", nil))
	return w.Code != http.StatusNotFound
}

// T5 (S4): the verification endpoint the door scanner calls is public, like the ticket
// route beside it — a scanner has no administrative token — and it is mounted with
// the ticket service, because the code it checks is the code that service signs.
func TestMountReservationMountsTheTicketVerificationRoute(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()

	if !ticketVerificationRouted(srv) {
		t.Fatal("the ticket verification route was not mounted although its schema is present")
	}
	if !ticketRouted(srv) || !paymentRouted(srv) || !reservationRouted(srv) {
		t.Fatal("mounting the verification route dropped a route next to it")
	}
	if !ticketQRRouted(srv) {
		t.Fatal("the ticket QR image route was not mounted next to the code it renders")
	}
	if adminRouted(srv) {
		t.Fatal("the verification route is behind the administrative guard, but a door scanner has no admin token")
	}
}

// T7 (S1, S3): the QR image the ticket page shows is served by the API from the code
// the ticket service signs, so the page and the PDF print one code and the web needs
// no QR dependency. It is public for the reason the detail route is — the ticket
// identifier is the capability — and it is mounted only when the ticket surface is.
func TestMountReservationMountsTheTicketQRImageRoute(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()

	if !ticketQRRouted(srv) {
		t.Fatal("the ticket QR image route was not mounted although its ticket schema is present")
	}
	if !ticketRouted(srv) || !ticketVerificationRouted(srv) || !reservationRouted(srv) {
		t.Fatal("mounting the QR image route dropped a route next to it")
	}
	if adminRouted(srv) {
		t.Fatal("the QR image route is behind the administrative guard, but the ticket page is the buyer's")
	}
}

// T5 (S4): a process that can serve tickets cannot sign them without a usable key, so
// it refuses to start instead of mounting a ticket route that would print codes
// nobody can trust — or, worse, codes signed with a key that ships in a repository.
// The refusal names the variable and never echoes a key.
func TestMountReservationRequiresAUsableTicketQRKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"no key at all", ""},
		{"a key below the documented minimum", "short-ticket-qr-key"},
		{"a blank key", strings.Repeat(" ", 40)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := config.Config{
				DatabaseURL:    adminTestURL,
				RedisURL:       "redis://127.0.0.1:6379/0",
				HoldTTL:        30 * time.Second,
				ExpiryInterval: 5 * time.Second,
				ExpiryBatch:    25,
				TicketQRKey:    testCase.key,
			}
			srv := testServer()
			proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
			if err == nil {
				proc.Close()
				t.Fatal("mountReservation mounted a ticket surface without a usable signing key")
			}
			if proc != nil {
				proc.Close()
				t.Fatal("a refused configuration returned a process")
			}
			if !strings.Contains(err.Error(), "API_TICKET_QR_KEY") && !strings.Contains(err.Error(), "signing key") {
				t.Fatalf("error = %q, want it to name the signing key setting", err)
			}
			if testCase.key != "" && strings.Contains(err.Error(), testCase.key) {
				t.Fatalf("error leaked the key: %v", err)
			}
			if ticketRouted(srv) || ticketVerificationRouted(srv) || ticketQRRouted(srv) {
				t.Fatal("a refused configuration still mounted a ticket route")
			}
		})
	}
}

// ticketPDFRouted reports whether the PDF download route is mounted, i.e. whether a
// download request reaches a handler instead of the mux's 404. The probe needs no
// database: a mounted route answers 503 (nothing could be read) rather than 404.
func ticketPDFRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/tickets/00000000-0000-0000-0000-00000000c0f1/pdf", nil))
	return w.Code != http.StatusNotFound
}

// withObjectStorage adds the local object-storage configuration the mount tests use.
// NewS3 performs no I/O, so the endpoint is never contacted: the mount decision is what
// is under test, not the bucket.
func withObjectStorage(cfg config.Config) config.Config {
	cfg.S3Endpoint = "http://127.0.0.1:1"
	cfg.S3Bucket = "cinema-m3-tickets"
	cfg.S3Region = "us-east-1"
	cfg.S3AccessKeyID = "floci"
	cfg.S3SecretAccessKey = "floci"
	cfg.S3PresignTTL = time.Minute
	return cfg
}

// T6 (S3, S5): with the ticket schema present and object storage configured, the
// download route is mounted at /api/v1 and the eager worker is wired next to it, so the
// confirmation page has a download to link to and the PDF is produced after the sale.
func TestMountReservationMountsTheTicketPDFRoute(t *testing.T) {
	cfg := withObjectStorage(config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	})
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()

	if proc.outboxStore == nil {
		t.Fatal("mountReservation wired no outbox store, so no PDF is produced after a sale")
	}
	if proc.pdfWorker == nil {
		t.Fatal("mountReservation wired no PDF worker next to the download route")
	}
	if !ticketPDFRouted(srv) {
		t.Fatal("the ticket PDF route was not mounted although its ticket schema and storage are present")
	}
	if !ticketRouted(srv) || !paymentRouted(srv) || !reservationRouted(srv) {
		t.Fatal("mounting the PDF route dropped a route next to it")
	}
	if adminRouted(srv) {
		t.Fatal("mounting the PDF route exposed the administrative subtree")
	}
}

// T6 (S3, S5): a deployment without object storage cannot generate or serve the
// artefact, so it advertises no download; the ticket detail route keeps working and the
// refusal is one informative line rather than an error per request.
func TestMountReservationSkipsTheTicketPDFRouteWithoutStorage(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring kept")
	}
	defer proc.Close()

	if proc.outboxStore != nil || proc.pdfWorker != nil {
		t.Fatal("a deployment without storage wired the PDF worker")
	}
	if ticketPDFRouted(srv) {
		t.Fatal("the PDF route was mounted without object storage")
	}
	if !ticketRouted(srv) || !reservationRouted(srv) {
		t.Fatal("the ticket detail and hold routes disappeared because storage is missing")
	}
	if !strings.Contains(logged.String(), "object storage not configured") {
		t.Fatalf("log = %q, want the one informative line about missing object storage", logged.String())
	}
	// Nothing of the storage configuration is echoed: the secret is a secret even when it
	// is the reason the route is missing.
	if strings.Contains(logged.String(), "floci") {
		t.Fatalf("log = %q, want no storage credential in it", logged.String())
	}
}

// T6 (S3, S5): the eager worker consumes the checkout's outbox, so it is wired only
// when that schema exists. A database with the ticket tables but without the checkout
// outbox keeps the download route — the lazy path generates the PDF on demand — and
// starts no worker that could only fail every tick.
func TestMountReservationSkipsThePDFWorkerWithoutTheCheckoutSchema(t *testing.T) {
	cfg := withObjectStorage(config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	})
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutAbsentSchema, ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()

	if proc.pdfWorker != nil || proc.outboxStore != nil {
		t.Fatal("a database without the checkout outbox started a PDF worker")
	}
	if !ticketPDFRouted(srv) {
		t.Fatal("the download route must stay mounted: it renders on demand without the worker")
	}
}

// T6 (S3, S5): a deployment whose database carries no tickets has nothing to print, so
// it mounts no download and starts no worker.
func TestMountReservationSkipsTheTicketPDFRouteWithoutTheTicketSchema(t *testing.T) {
	cfg := withObjectStorage(config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	})
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketAbsentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring kept")
	}
	defer proc.Close()

	if proc.pdfWorker != nil || proc.outboxStore != nil {
		t.Fatal("a database without tickets started a PDF worker")
	}
	if ticketPDFRouted(srv) {
		t.Fatal("the PDF route was mounted without the ticket schema")
	}
	if !reservationRouted(srv) || !paymentRouted(srv) {
		t.Fatal("the reservation and payment routes disappeared because the ticket schema is missing")
	}
}

// The catalogue and reservation read probes stand in for the schema check of the two
// M3.5 reads, so their mount decision is testable without a database.
func cataloguePresentSchema(context.Context, *catalog.PostgresStore) (bool, error) {
	return true, nil
}
func catalogueAbsentSchema(context.Context, *catalog.PostgresStore) (bool, error) {
	return false, nil
}
func catalogueFailingSchema(context.Context, *catalog.PostgresStore) (bool, error) {
	return false, errors.New("database unreachable")
}

// catalogueRouted reports whether the public catalogue route is mounted, i.e. whether a
// catalogue request reaches the handler instead of a 404. A mounted route cannot answer
// 404 for the catalogue — an empty catalogue is a 200 with an empty list — and the store
// here points at a database that does not answer, so the probe never needs a live one.
func catalogueRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	return w.Code != http.StatusNotFound
}

// catalogueScreeningRouted reports whether the single screening read is mounted. It is
// mounted by the same wiring decision as the catalogue list — one read model, one
// probe — so this asks the route a buyer arrives at from the list.
func catalogueScreeningRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/events/00000000-0000-0000-0000-00000000e0f1", nil))
	return w.Code != http.StatusNotFound
}

// reservationReadRouted reports whether the confirmation read is mounted, i.e. whether
// a GET on the reservation subtree reaches the handler instead of a 404.
func reservationReadRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/reservations/00000000-0000-0000-0000-00000000c0f1", nil))
	return w.Code != http.StatusNotFound
}

// cancelRouted reports whether the cancel route is mounted, i.e. whether a POST on the
// reservation's cancel subtree reaches the handler instead of a 404.
func cancelRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/api/v1/reservations/00000000-0000-0000-0000-00000000c0f1/cancel", nil))
	return w.Code != http.StatusNotFound
}

// T1 (S5): the two reads the public web needs are part of the customer surface, so they
// are mounted at /api/v1 next to the hold, payment and ticket routes and outside the
// administrative guard: a stranger has no event id to reach the seat page with and no
// administrative token to present.
func TestMountReservationMountsTheCatalogueAndReservationReads(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema,
		ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()

	if proc.catalogStore == nil {
		t.Fatal("mountReservation built no catalogue store next to the ticket store")
	}
	if !catalogueRouted(srv) {
		t.Fatal("the catalogue route was not mounted although its schema is present")
	}
	if !catalogueScreeningRouted(srv) {
		t.Fatal("the single screening read was not mounted although its schema is present")
	}
	if !reservationReadRouted(srv) {
		t.Fatal("the reservation read was not mounted although its schema is present")
	}
	// T4 (S10): cancelling is mounted with the read it answers in the same shape: a cancel
	// that had nowhere to read the order from could only fail.
	if !cancelRouted(srv) {
		t.Fatal("the cancel route was not mounted although its schema is present")
	}
	// The routes the customer surface already served are still mounted next to them.
	if !reservationRouted(srv) || !paymentRouted(srv) || !ticketRouted(srv) {
		t.Fatal("mounting the two reads dropped a route next to them")
	}
	if adminRouted(srv) {
		t.Fatal("mounting the two reads exposed the administrative subtree")
	}
}

// T1 (S5): a database without the objects the reads touch advertises neither route — a
// catalogue route that could only count seats it cannot read, or a confirmation read
// that could not answer an amount or a ticket, is worse than an absent one. The rest of
// the customer surface keeps working, with one informative line instead of an error per
// request.
func TestMountReservationSkipsTheReadsWithoutTheirSchema(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema,
		ticketPresentSchema, catalogueAbsentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring kept")
	}
	defer proc.Close()

	if proc.catalogStore != nil {
		t.Fatal("mountReservation kept a catalogue store without the schema its reads touch")
	}
	if catalogueRouted(srv) || reservationReadRouted(srv) {
		t.Fatal("the catalogue and reservation read routes were mounted without their schema")
	}
	if cancelRouted(srv) {
		t.Fatal("the cancel route was mounted without the read whose shape it answers")
	}
	if catalogueScreeningRouted(srv) {
		t.Fatal("the single screening read was mounted without the schema it reads")
	}
	if !reservationRouted(srv) || !paymentRouted(srv) || !ticketRouted(srv) {
		t.Fatal("the rest of the customer surface disappeared because the read schema is missing")
	}
	if !strings.Contains(logged.String(), "catalogue schema not present") {
		t.Fatalf("log = %q, want the one informative line about the missing read schema", logged.String())
	}
}

// A read schema probe that cannot answer is a startup failure: mounting a catalogue or a
// confirmation read whose schema state is unknown would be guessing, and the refusal
// never echoes the database URL or its password.
func TestMountReservationFailsWhenTheReadProbeCannotAnswer(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
	}
	srv := testServer()
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema,
		ticketPresentSchema, catalogueFailingSchema)
	if err == nil {
		t.Fatal("mountReservation returned no error for an unanswerable read schema probe")
	}
	if proc != nil {
		t.Fatal("mountReservation returned a process for an unanswerable read schema probe")
	}
	if catalogueRouted(srv) || reservationReadRouted(srv) || ticketRouted(srv) || reservationRouted(srv) {
		t.Fatal("a surface was mounted although the read schema state is unknown")
	}
	if catalogueScreeningRouted(srv) {
		t.Fatal("the single screening read was mounted although the read schema state is unknown")
	}
	for _, leaked := range []string{"s3cr3t-pw", "postgres://"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error leaked %q: %v", leaked, err)
		}
	}
}

// T2 (S4): the two screening reads and the price write are part of the administrative
// subtree, so they are mounted behind the same local-only guard as the venue routes.
// An unauthorized request is indistinguishable from a missing route, and a guarded one
// reaches the new handler.
func TestAdminScreeningRoutesAreBehindTheGuard(t *testing.T) {
	cfg := config.Config{Environment: "development", LocalAdminEnabled: true, LocalAdminToken: "admin-guard-token"}
	srv := httpserver.New(cfg, health.New(health.ProcessProbe{}), discardLogger())
	srv.WithAdmin(adminapi.New(venue.NewService(nil)).Mux())

	call := func(method, target, token string) int {
		request := httptest.NewRequest(method, target, strings.NewReader(`{"priceCents":1250}`))
		request.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, request)
		return w.Code
	}

	for _, target := range []string{"/api/v1/admin/events", "/api/v1/admin/events/not-an-event"} {
		if status := call(http.MethodGet, target, ""); status != http.StatusNotFound {
			t.Fatalf("GET %s without a token = %d, want 404 like a missing route", target, status)
		}
		if status := call(http.MethodGet, target, "wrong-token"); status != http.StatusNotFound {
			t.Fatalf("GET %s with a wrong token = %d, want 404 like a missing route", target, status)
		}
	}
	if status := call(http.MethodPatch, "/api/v1/admin/events/not-an-event", ""); status != http.StatusNotFound {
		t.Fatalf("PATCH without a token = %d, want 404 like a missing route", status)
	}
	// A guarded request reaches the handler: the malformed identifier is the handler's
	// rejection, which proves the route is present behind the guard without needing a
	// database or a service.
	if status := call(http.MethodPatch, "/api/v1/admin/events/not-an-event", "admin-guard-token"); status != http.StatusBadRequest {
		t.Fatalf("PATCH with the token = %d, want the handler's 400", status)
	}
	if status := call(http.MethodGet, "/api/v1/admin/events/not-an-event", "admin-guard-token"); status != http.StatusBadRequest {
		t.Fatalf("GET one event with the token = %d, want the handler's 400", status)
	}
}

// S5 — without a mail provider the email consumer is not built at all: there is nothing to
// send with, so a worker could only fail every tick. The process says so once, names the
// setting that is missing, and keeps serving; this is the state every gate runs in.
func TestMountReservationLeavesTheTicketEmailOffWithoutAProvider(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema, ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring")
	}
	defer proc.Close()

	if proc.emailWorker != nil || proc.emailStore != nil {
		t.Fatal("a deployment with no mail provider wired the ticket email worker")
	}
	// And it advertises no way to send mail either: the resend route is mounted from the same
	// decision as the consumer, because a deployment that cannot send must never answer 202.
	if ticketEmailRouted(srv) {
		t.Fatal("a deployment with no mail provider advertised the ticket resend route")
	}
	if !strings.Contains(logged.String(), "ticket email disabled") {
		t.Fatalf("log = %q, want the one line saying the ticket email is disabled", logged.String())
	}
	if !strings.Contains(logged.String(), "API_SMTP_ADDR") || !strings.Contains(logged.String(), "API_EMAIL_FROM") {
		t.Fatalf("log = %q, want it to name the settings that switch the ticket email on", logged.String())
	}
	// Nothing was disabled that the buyer needs: the routes next to it are still mounted.
	if !ticketRouted(srv) || !paymentRouted(srv) || !reservationRouted(srv) {
		t.Fatal("a deployment with no mail provider lost a route it does not need a provider for")
	}
}

// S3/S5 — the link the email carries is built from the signed ticket link and the public
// web origin. A worker built without a signer gets an error and therefore logs and retries
// instead of mailing a link that opens nothing; the refusal never carries a token.
func TestTicketLinkBuilderRefusesWithoutASigner(t *testing.T) {
	link, err := ticketLinkBuilder("https://cinema.test", nil)("00000000-0000-0000-0000-00000000e0c1")
	if err == nil {
		t.Fatalf("the builder answered a link (%q) without a signer, want an error", link)
	}
	if link != "" {
		t.Fatalf("the builder returned %q next to its error, want no link at all", link)
	}
	if !strings.Contains(err.Error(), "API_TICKET_LINK_KEY") {
		t.Fatalf("error = %q, want it to name the setting that is missing", err)
	}
}

// S3 — the built link is the public web's ticket route with the reservation's signed token
// on it, so the address the buyer receives opens the order the sale recorded.
func TestTicketLinkBuilderSignsTheReservation(t *testing.T) {
	const reservationID = "00000000-0000-0000-0000-00000000e0c1"
	// A trailing slash is trimmed rather than doubled: the link route appends its own.
	signer, err := tickets.NewLinkSigner(testTicketQRKey, time.Hour)
	if err != nil {
		t.Fatalf("NewLinkSigner returned error: %v", err)
	}

	link, err := ticketLinkBuilder("https://cinema.test/", signer)(reservationID)
	if err != nil {
		t.Fatalf("the builder returned error: %v", err)
	}
	prefix := "https://cinema.test/t/"
	if !strings.HasPrefix(link, prefix) {
		t.Fatalf("link = %q, want it to begin with %q", link, prefix)
	}
	claims, err := signer.Verify(strings.TrimPrefix(link, prefix), time.Now())
	if err != nil {
		t.Fatalf("the link the builder minted does not verify: %v", err)
	}
	if claims.ReservationID != reservationID {
		t.Fatalf("link names reservation %q, want %q", claims.ReservationID, reservationID)
	}
}

// identityLinkRouted reports whether the login link route is mounted, i.e. whether a POST on
// it reaches the handler instead of a 404. An empty body is rejected by the transport before
// any I/O, so this probe needs no database and no mail server.
func identityLinkRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/identity/links", strings.NewReader("")))
	return w.Code != http.StatusNotFound
}

// identitySessionRouted reports whether the redeem route is mounted, the same way.
func identitySessionRouted(srv *httpserver.Server) bool {
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/identity/sessions", strings.NewReader("")))
	return w.Code != http.StatusNotFound
}

// T2 (S6): without its signing key the login surface is not built at all. It answers one
// line naming the setting that is missing and keeps serving: this is the state every gate
// runs in, and it must be the state every deployment that predates identity keeps.
func TestMountReservationLeavesTheIdentityLoginOffWithoutItsKey(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:    adminTestURL,
		RedisURL:       "redis://127.0.0.1:6379/0",
		HoldTTL:        30 * time.Second,
		ExpiryInterval: 5 * time.Second,
		ExpiryBatch:    25,
		TicketQRKey:    testTicketQRKey,
	}
	srv := testServer()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema,
		ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring kept")
	}
	defer proc.Close()

	if proc.identityStore != nil {
		t.Fatal("a deployment with no identity link key opened the identity store")
	}
	if identityLinkRouted(srv) || identitySessionRouted(srv) {
		t.Fatal("a deployment with no identity link key advertised the login routes")
	}
	if !strings.Contains(logged.String(), "identity login disabled") {
		t.Fatalf("log = %q, want the one line saying the identity login is disabled", logged.String())
	}
	if !strings.Contains(logged.String(), "API_IDENTITY_LINK_KEY") {
		t.Fatalf("log = %q, want it to name the setting that switches the login on", logged.String())
	}
	// Nothing else was dropped: a deployment without identity behaves exactly as it did
	// before it existed.
	if !ticketRouted(srv) || !paymentRouted(srv) || !reservationRouted(srv) {
		t.Fatal("a deployment without identity lost a route it does not need identity for")
	}
}

// T2 (D1, S6): identity logs in by mail, so a deployment with no provider has no login
// either. The key is configured and the surface is still not built, because an issuer with
// nowhere to send could only refuse every request it accepted.
func TestMountReservationLeavesTheIdentityLoginOffWithoutAMailProvider(t *testing.T) {
	cfg := config.Config{
		DatabaseURL:     adminTestURL,
		RedisURL:        "redis://127.0.0.1:6379/0",
		HoldTTL:         30 * time.Second,
		ExpiryInterval:  5 * time.Second,
		ExpiryBatch:     25,
		TicketQRKey:     testTicketQRKey,
		IdentityLinkKey: testIdentityLinkKey,
		IdentityLinkTTL: 15 * time.Minute,
	}
	srv := testServer()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema,
		ticketPresentSchema, cataloguePresentSchema)
	if err != nil {
		t.Fatalf("mountReservation returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("mountReservation returned no process, want the reservation wiring kept")
	}
	defer proc.Close()

	if proc.identityStore != nil {
		t.Fatal("a deployment with no mail provider opened the identity store")
	}
	if identityLinkRouted(srv) || identitySessionRouted(srv) {
		t.Fatal("a deployment with no mail provider advertised the login routes")
	}
	if !strings.Contains(logged.String(), "identity login disabled") {
		t.Fatalf("log = %q, want the one line saying the identity login is disabled", logged.String())
	}
	if !strings.Contains(logged.String(), "API_SMTP_ADDR") || !strings.Contains(logged.String(), "API_EMAIL_FROM") {
		t.Fatalf("log = %q, want it to name the mail settings the login depends on", logged.String())
	}
}

// T2 (S2): a key this deployment cannot sign with builds no surface either, and the refusal
// names the setting and never the key: a key is a secret even when it is wrong.
func TestIdentitySurfaceRefusesAnUnusableKey(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	sender, err := email.NewSMTP("127.0.0.1:1025", "tickets@cinema.test")
	if err != nil {
		t.Fatalf("NewSMTP() = unexpected error %v", err)
	}

	surface, err := newIdentitySurface(config.Config{
		DatabaseURL:     adminTestURL,
		IdentityLinkKey: "too-short",
		IdentityLinkTTL: 15 * time.Minute,
		PublicWebURL:    "https://cinema.test",
		EmailEnabled:    true,
	}, sender, nil, logger)
	if err != nil {
		t.Fatalf("newIdentitySurface returned error: %v", err)
	}
	if surface != nil {
		surface.store.Close()
		t.Fatal("newIdentitySurface built a surface from a key it cannot sign with")
	}
	if !strings.Contains(logged.String(), "API_IDENTITY_LINK_KEY") {
		t.Fatalf("log = %q, want it to name the setting that is unusable", logged.String())
	}
	if strings.Contains(logged.String(), "too-short") {
		t.Fatalf("log = %q, want it to never echo the key", logged.String())
	}
}

// T2 (S2): the login mail's adapter is the ticket mail's sender, so one provider serves both
// messages and a second adapter never has to be kept in step.
func TestIdentityMailerSendsThroughTheTicketSender(t *testing.T) {
	sender := &recordingSender{}
	mailer := identityMailer{sender: sender}

	err := mailer.Send(context.Background(), identity.Mail{
		To:      "ana@example.test",
		Subject: "Your CinemaTicketing sign-in link",
		Body:    "https://cinema.test/in/token",
	})
	if err != nil {
		t.Fatalf("Send() = unexpected error %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("the sender was handed %d message(s), want exactly one", len(sender.messages))
	}
	got := sender.messages[0]
	if got.To != "ana@example.test" || got.Subject != "Your CinemaTicketing sign-in link" ||
		got.Body != "https://cinema.test/in/token" {
		t.Fatalf("message = %+v, want the login mail's fields unchanged", got)
	}
}

// recordingSender records what an adapter asked a provider to send.
type recordingSender struct{ messages []email.Message }

func (s *recordingSender) Send(_ context.Context, message email.Message) error {
	s.messages = append(s.messages, message)
	return nil
}

// T2 (S2): the link prefix is the public web's, with one slash between the origin and the
// route the web serves, whatever the configured origin ends with.
func TestIdentityLinkBaseIsThePublicWebOrigin(t *testing.T) {
	cases := map[string]string{
		"https://cinema.test":  "https://cinema.test/in/",
		"https://cinema.test/": "https://cinema.test/in/",
	}
	for origin, want := range cases {
		if got := identityLinkBase(origin); got != want {
			t.Fatalf("identityLinkBase(%q) = %q, want %q", origin, got, want)
		}
	}
}
