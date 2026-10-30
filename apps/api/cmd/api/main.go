// Command api is the Go API platform entrypoint for the cinema ticketing
// backend.
//
// It wires configuration, structured logging, the health service, and the HTTP
// server with graceful shutdown, and pins the module boundaries into the build.
// M1 adds the local-only administrative venue API, which is mounted only when
// local administration is explicitly enabled. M2 adds the customer reservation
// surface with its expiry worker, and M3 adds the checkout route that pays a held
// reservation.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"cinema.local/ticketing/apps/api/internal/adminapi"
	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/log"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/reservation"
	"cinema.local/ticketing/apps/api/internal/venue"

	// Module boundaries: wired into the build so the skeleton stays explicit
	// and any module that fails to compile fails the whole binary.
	_ "cinema.local/ticketing/apps/api/modules/catalog"
	_ "cinema.local/ticketing/apps/api/modules/identity"
	_ "cinema.local/ticketing/apps/api/modules/layout"
	_ "cinema.local/ticketing/apps/api/modules/payment"
	_ "cinema.local/ticketing/apps/api/modules/platform"
	_ "cinema.local/ticketing/apps/api/modules/reporting"
	_ "cinema.local/ticketing/apps/api/modules/reservation"
	_ "cinema.local/ticketing/apps/api/modules/ticketing"
)

func main() {
	os.Exit(run())
}

// run starts the API and returns the process exit code. It keeps the store
// lifetime and the exit code in one place so shutdown still releases database
// connections before the process ends.
func run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Default().Error("load configuration", "error", err)
		return 1
	}

	logger, err := log.New(cfg.LogLevel, os.Stdout)
	if err != nil {
		slog.Default().Error("init logger", "error", err)
		return 1
	}

	probes := []health.Probe{health.ProcessProbe{}}
	for _, dependency := range cfg.Dependencies {
		probes = append(probes, health.NewTCPProbe(dependency, dependencyAddress(dependency)))
	}
	svc := health.New(probes...)

	srv := httpserver.New(cfg, svc, logger)

	if adminStore, err := mountAdmin(cfg, srv, logger); err != nil {
		logger.Error("configure local administration", "error", err)
		return 1
	} else if adminStore != nil {
		defer adminStore.Close()
	}

	reservationProcess, err := mountReservation(cfg, srv, logger)
	if err != nil {
		logger.Error("configure the reservation surface", "error", err)
		return 1
	}
	defer reservationProcess.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The sweep belongs to the process: it starts with it, keeps its deadline on
	// the PostgreSQL clock, and stops when the process context is cancelled.
	var sweepStopped chan struct{}
	if reservationProcess != nil {
		sweepStopped = make(chan struct{})
		go func() {
			defer close(sweepStopped)
			reservationProcess.sweeper.Run(ctx)
		}()
	}

	logger.Info("starting api", "addr", cfg.Addr, "log_level", cfg.LogLevel)
	serveErr := srv.Serve(ctx)
	if serveErr != nil {
		// A serve error (a taken port, for example) never cancels the signal
		// context, so the worker would keep running until the shutdown budget
		// expired. Cancel it for the same reason the server stopped.
		stop()
	}

	if sweepStopped != nil {
		// Wait for the worker before the deferred Close releases its pools, so
		// shutdown never sweeps a closed store.
		select {
		case <-sweepStopped:
		case <-time.After(cfg.ShutdownTimeout):
			logger.Warn("expiry sweep did not stop within the shutdown timeout")
		}
	}

	if serveErr != nil {
		logger.Error("server stopped with error", "error", serveErr)
		return 1
	}
	logger.Info("api stopped")
	return 0
}

// expirySweepTimeout bounds one durable expiry sweep, including its clock read,
// so a stalled database cannot wedge the worker.
const expirySweepTimeout = 30 * time.Second

// expirySweepMaxBatches bounds how many batches one tick may run. The next tick
// continues from where this one stopped, so a backlog is drained across ticks
// instead of holding the worker in one long loop.
const expirySweepMaxBatches = 8

// startupTimeout bounds the database work that happens before the server starts
// listening. Without it a database that accepts the connection but never answers
// would stall startup forever, which is worse than failing fast.
const startupTimeout = 10 * time.Second

// requiredMigrations lists the migration versions the customer surface needs. The
// reservation surface is closed until 0003 and 0004 are applied, and the checkout
// route is closed until 0005 and 0006 are applied as well, so a database without the
// checkout schema — or without the one-open-attempt guard — advertises no payment
// route instead of failing every request.
const requiredMigrations = "0003_reservation, 0004_idempotency, 0005_checkout, 0006_one_open_payment_attempt"

// reservationProcess owns what the public reservation surface, the checkout and
// the expiry worker need, so one Close releases every pool on shutdown.
type reservationProcess struct {
	store         *reservation.PostgresStore
	checkoutStore *checkout.PostgresStore
	redis         *redis.Client
	sweeper       *reservation.Sweeper
}

// Close releases the pooled connections. It is nil-safe so the process wiring can
// defer it unconditionally.
func (p *reservationProcess) Close() {
	if p == nil {
		return
	}
	if p.store != nil {
		p.store.Close()
	}
	if p.checkoutStore != nil {
		p.checkoutStore.Close()
	}
	if p.redis != nil {
		_ = p.redis.Close()
	}
}

// mountReservation wires the customer facing surface: the durable reservation
// store, the shared coordination layer, the hold service over the configured
// window, the public routes, the checkout route and the durable expiry worker.
//
// It mounts nothing unless a database URL is configured, because a hold without
// durable storage could not be honoured; the default M0/M1 configuration
// therefore keeps its routes and lifetime unchanged. The returned process must be
// closed on shutdown and is nil when nothing was mounted. A failure is reported
// without echoing a connection URL, which may carry credentials.
func mountReservation(cfg config.Config, srv *httpserver.Server, logger *slog.Logger) (*reservationProcess, error) {
	return mountReservationWithProbes(cfg, srv, logger, reservationSchemaProbe, checkoutSchemaProbe)
}

// schemaProbe reports whether the database carries the reservation tables, and
// checkoutProbe whether it carries the checkout objects. They are parameters so
// both mount decisions can be tested without a live database.
type schemaProbe func(context.Context, *reservation.PostgresStore) (bool, error)

type checkoutProbe func(context.Context, *checkout.PostgresStore) (bool, error)

// reservationSchemaProbe is the production probe: the store's own check.
func reservationSchemaProbe(ctx context.Context, store *reservation.PostgresStore) (bool, error) {
	return store.ReservationSchemaPresent(ctx)
}

// checkoutSchemaProbe is the production probe of the M3 objects the payment route
// writes: the payment attempt, the ticket and the outbox table, the event price the
// checkout charges, and the one-open-attempt index that keeps a second payment from
// reaching the provider. A database missing any of them keeps the route closed.
func checkoutSchemaProbe(ctx context.Context, store *checkout.PostgresStore) (bool, error) {
	return store.CheckoutSchemaPresent(ctx)
}

// newPaymentGateway builds the payment boundary the checkout is wired with: the
// simulated provider, which never declines. A decline is a test scenario built by
// constructing an adapter with a decline rule, never the default the process runs
// with, so a real 12,13 EUR ticket is an ordinary purchase (design S8 keeps the
// provider replaceable: only this function changes for a real one).
func newPaymentGateway() payment.PaymentGateway { return payment.NewSimulated() }

// mountReservationWithProbes is mountReservation with both schema checks injected.
func mountReservationWithProbes(cfg config.Config, srv *httpserver.Server, logger *slog.Logger,
	probe schemaProbe, checkoutSchema checkoutProbe) (*reservationProcess, error) {
	if cfg.DatabaseURL == "" {
		return nil, nil
	}
	// config.LoadFrom rejects these, but a programmatically built Config must not
	// reach the customer surface with a window it cannot honour or a sweeper it
	// cannot start.
	if cfg.HoldTTL <= 0 {
		return nil, fmt.Errorf("reservation surface requires a positive hold window, got %s", cfg.HoldTTL)
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelStartup()
	store, err := reservation.NewPostgresStore(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open reservation store: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}

	startupCtx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	present, err := probe(startupCtx, store)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("check the reservation schema: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}
	if !present {
		// Fail closed without noise: no routes and no worker until the schema
		// that backs them is applied. One line, once, instead of an error per tick.
		store.Close()
		logger.Info("reservation schema not present; reservation surface disabled",
			"required", requiredMigrations)
		return nil, nil
	}

	options, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("parse the coordination URL: %s", redactDatabaseURL(err.Error(), cfg.RedisURL))
	}
	client := redis.NewClient(options)
	svc := reservation.NewService(reservation.NewCoordinator(client), store, cfg.HoldTTL)

	sweeper, err := reservation.NewSweeper(svc, store, reservation.SweeperConfig{
		Interval:   cfg.ExpiryInterval,
		Batch:      cfg.ExpiryBatch,
		Timeout:    expirySweepTimeout,
		MaxBatches: expirySweepMaxBatches,
		Logger:     logger,
	})
	if err != nil {
		_ = client.Close()
		store.Close()
		return nil, fmt.Errorf("configure the expiry sweep: %w", err)
	}

	// The checkout route needs the M3 objects on top of the reservation tables. Its
	// store is opened once, and closed again when the schema that backs the route is
	// missing, so nothing advertises a payment it cannot record. It gets its own
	// startup budget, so the reservation store's remaining deadline cannot decide
	// whether the checkout route is mounted.
	checkoutCtx, cancelCheckout := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelCheckout()
	checkoutStore, err := checkout.NewPostgresStore(checkoutCtx, cfg.DatabaseURL)
	if err != nil {
		_ = client.Close()
		store.Close()
		return nil, fmt.Errorf("open checkout store: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}
	checkoutPresent, err := checkoutSchema(checkoutCtx, checkoutStore)
	if err != nil {
		checkoutStore.Close()
		_ = client.Close()
		store.Close()
		return nil, fmt.Errorf("check the checkout schema: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}

	// payments is a nil interface unless the checkout schema is present, which is
	// exactly what keeps the route unmounted.
	var payments publicapi.PaymentService
	if checkoutPresent {
		payments = checkout.NewService(newPaymentGateway(), checkoutStore, reservation.NewCoordinator(client), payment.ProviderName)
	} else {
		checkoutStore.Close()
		checkoutStore = nil
		logger.Info("checkout schema not present; payment route disabled", "required", "0005_checkout")
	}

	srv.WithPublic(publicapi.New(svc).WithPayments(payments).Mux())
	logger.Info("public reservation API mounted",
		"prefix", "/api/v1", "hold_ttl", cfg.HoldTTL, "expiry_interval", cfg.ExpiryInterval,
		"expiry_batch", cfg.ExpiryBatch, "checkout", checkoutPresent)
	return &reservationProcess{store: store, checkoutStore: checkoutStore, redis: client, sweeper: sweeper}, nil
}

// mountAdmin wires the administrative venue API behind the local-development
// guard. It mounts nothing unless local administration is enabled, so the
// default and production configurations expose no administrative route at all.
//
// The returned store must be closed on shutdown; it is nil when nothing was
// mounted. A store that cannot be opened is an error, reported without echoing
// the database URL, which may carry credentials.
func mountAdmin(cfg config.Config, srv *httpserver.Server, logger *slog.Logger) (*venue.PostgresStore, error) {
	if !cfg.LocalAdminEnabled {
		return nil, nil
	}
	// config.LoadFrom already rejects enabled local administration without a
	// database URL; the check keeps a programmatically built Config from opening
	// a pool from ambient defaults.
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("local administration requires a database URL")
	}

	store, err := venue.NewPostgresStore(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open venue store for local administration: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}

	srv.WithAdmin(adminapi.New(venue.NewService(store)).Mux())
	logger.Info("local administrative venue API mounted", "prefix", "/api/v1/admin/")
	return store, nil
}

// redactDatabaseURL strips the database URL, and any credentials it embeds, from
// a diagnostic message before it reaches a log. pgx quotes the connection string
// in its parse errors (sometimes with the password already masked) and may report
// the userinfo or the password alone, so the message is scrubbed of the exact
// configured URL as well as anything URL-shaped. The actionable reason the driver
// gave is preserved.
func redactDatabaseURL(message, databaseURL string) string {
	if databaseURL != "" {
		message = strings.ReplaceAll(message, databaseURL, "<redacted>")
		if parsed, err := url.Parse(databaseURL); err == nil && parsed.User != nil {
			secrets := []string{parsed.User.String()}
			if password, ok := parsed.User.Password(); ok {
				secrets = append(secrets, password)
			}
			for _, secret := range secrets {
				if secret != "" {
					message = strings.ReplaceAll(message, secret, "<redacted>")
				}
			}
		}
	}
	// The driver may render the connection string in a form that no longer matches
	// the configured URL, for example with the password masked, so replace every
	// URL-shaped span as well.
	return urlShaped.ReplaceAllString(message, "<redacted>")
}

var urlShaped = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://\S*`)

func dependencyAddress(dependency string) string {
	key := "API_" + strings.ToUpper(dependency) + "_ADDR"
	if address := os.Getenv(key); address != "" {
		return address
	}
	defaults := map[string]string{
		"postgresql": "127.0.0.1:5432",
		"redis":      "127.0.0.1:6379",
	}
	return defaults[dependency]
}
