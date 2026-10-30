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
	"cinema.local/ticketing/apps/api/internal/tickets"
)

const adminTestURL = "postgres://cinema:s3cr3t-pw@127.0.0.1:5432/cinema?sslmode=disable"

// testTicketQRKey is the throwaway signing key the mount tests configure: long
// enough for the documented minimum, and never the development default.
const testTicketQRKey = "mount-test-ticket-qr-key-0123456789abcdef"

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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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
			proc, err := mountReservationWithProbes(tc.cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutAbsentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutFailingSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema, ticketAbsentSchema)
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
	if ticketRouted(srv) || ticketQRRouted(srv) {
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketFailingSchema)
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, absentSchema, checkoutPresentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), failingSchema, checkoutPresentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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
			proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketPresentSchema)
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

	proc, err := mountReservationWithProbes(cfg, srv, logger, presentSchema, checkoutPresentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutAbsentSchema, ticketPresentSchema)
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
	proc, err := mountReservationWithProbes(cfg, srv, discardLogger(), presentSchema, checkoutPresentSchema, ticketAbsentSchema)
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
