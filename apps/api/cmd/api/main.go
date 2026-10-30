// Command api is the Go API platform entrypoint for the cinema ticketing
// backend.
//
// It wires configuration, structured logging, the health service, and the HTTP
// server with graceful shutdown, and pins the module boundaries into the build.
// M1 adds the local-only administrative venue API, which is mounted only when
// local administration is explicitly enabled. M2 adds the customer reservation
// surface with its expiry worker, and M3 adds the checkout route that pays a held
// reservation, the ticket detail route the confirmation page and the PDF read, the
// ticket PDF download route and the worker that stores those PDFs in the object
// bucket. M3.5 adds the reads the public web needs: the catalogue of screenings a buyer
// can buy, one screening of that catalogue for the seat page, and the confirmation's read
// of one reservation.
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
	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/log"
	"cinema.local/ticketing/apps/api/internal/payment"
	"cinema.local/ticketing/apps/api/internal/pdf"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/reservation"
	"cinema.local/ticketing/apps/api/internal/storage"
	"cinema.local/ticketing/apps/api/internal/tickets"
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

	// The ticket PDF worker belongs to the process in the same way: it stores the
	// document of every ticket the checkout issued, and it stops with the process so
	// shutdown never polls a closed pool. A deployment without storage or without the
	// ticket schema starts none, and the download route generates on demand instead.
	var pdfWorkerStopped chan struct{}
	if reservationProcess != nil && reservationProcess.pdfWorker != nil {
		pdfWorkerStopped = make(chan struct{})
		go func() {
			defer close(pdfWorkerStopped)
			reservationProcess.pdfWorker.Run(ctx)
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

	// Wait for the workers before the deferred Close releases their pools, so shutdown
	// never sweeps or stores through a closed store. They share one budget: a process
	// that is stopping has no time to give each of them the whole timeout.
	shutdownDeadline := time.Now().Add(cfg.ShutdownTimeout)
	for _, worker := range []struct {
		name    string
		stopped chan struct{}
	}{
		{"expiry sweep", sweepStopped},
		{"ticket PDF worker", pdfWorkerStopped},
	} {
		if worker.stopped == nil {
			continue
		}
		select {
		case <-worker.stopped:
		case <-time.After(time.Until(shutdownDeadline)):
			logger.Warn(worker.name + " did not stop within the shutdown timeout")
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

// The ticket PDF worker's cadence and bounds. Five seconds is short enough that the
// document is normally in the bucket before the buyer opens the confirmation page and
// long enough that an idle deployment costs one query every five seconds; the batch and
// the deadline keep one pass bounded, so a backlog is drained across ticks instead of
// holding the worker in a long loop.
const (
	pdfWorkerInterval = 5 * time.Second
	pdfWorkerBatch    = 20
	pdfWorkerTimeout  = 30 * time.Second
)

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
// and ticket detail routes are closed until 0005 and 0006 are applied as well, so a
// database without the checkout schema — or without the one-open-attempt guard —
// advertises no payment route and no ticket route instead of failing every request.
const requiredMigrations = "0003_reservation, 0004_idempotency, 0005_checkout, 0006_one_open_payment_attempt"

// reservationProcess owns what the public reservation surface, the checkout, the
// ticket detail read, the ticket PDF archive, the catalogue and confirmation reads and
// the expiry worker need, so one Close releases every pool on shutdown.
type reservationProcess struct {
	store         *reservation.PostgresStore
	checkoutStore *checkout.PostgresStore
	ticketStore   *tickets.PostgresStore
	catalogStore  *catalog.PostgresStore
	outboxStore   *pdf.OutboxStore
	redis         *redis.Client
	sweeper       *reservation.Sweeper
	pdfWorker     *pdf.Worker
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
	if p.ticketStore != nil {
		p.ticketStore.Close()
	}
	if p.catalogStore != nil {
		p.catalogStore.Close()
	}
	if p.outboxStore != nil {
		p.outboxStore.Close()
	}
	if p.redis != nil {
		_ = p.redis.Close()
	}
}

// mountReservation wires the customer facing surface: the durable reservation
// store, the shared coordination layer, the hold service over the configured
// window, the public routes, the checkout route, the ticket detail route and the
// durable expiry worker.
//
// It mounts nothing unless a database URL is configured, because a hold without
// durable storage could not be honoured; the default M0/M1 configuration
// therefore keeps its routes and lifetime unchanged. The returned process must be
// closed on shutdown and is nil when nothing was mounted. A failure is reported
// without echoing a connection URL, which may carry credentials.
func mountReservation(cfg config.Config, srv *httpserver.Server, logger *slog.Logger) (*reservationProcess, error) {
	return mountReservationWithProbes(cfg, srv, logger, reservationSchemaProbe, checkoutSchemaProbe, ticketSchemaProbe, catalogueSchemaProbe)
}

// schemaProbe reports whether the database carries the reservation tables,
// checkoutProbe whether it carries the checkout objects, and ticketsProbe whether it
// carries what the ticket detail read touches. They are parameters so every mount
// decision can be tested without a live database.
type schemaProbe func(context.Context, *reservation.PostgresStore) (bool, error)

type checkoutProbe func(context.Context, *checkout.PostgresStore) (bool, error)

type ticketsProbe func(context.Context, *tickets.PostgresStore) (bool, error)

// readsProbe reports whether the database carries the objects the catalogue and the
// confirmation's read touch.
type readsProbe func(context.Context, *catalog.PostgresStore) (bool, error)

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

// ticketSchemaProbe is the production probe of the objects the ticket detail read
// touches: the tickets a checkout issued, the captured payment attempt its amount
// comes from, and the venue, event and layout tables the screening is read through.
// It is its own probe rather than a reuse of the checkout one because the ticket
// route does not read the outbox or the event price, so requiring them would close
// the route on a database that can serve it perfectly well.
func ticketSchemaProbe(ctx context.Context, store *tickets.PostgresStore) (bool, error) {
	return store.TicketDetailSchemaPresent(ctx)
}

// catalogueSchemaProbe is the production probe of the objects the two public reads
// touch: the venue, film and event tables the catalogue answers from, the event seats
// and holds its availability count reads, and the tickets and captured payment attempts
// the confirmation's read needs. It is its own probe rather than a reuse of the ticket
// one because the catalogue also counts `cinema.reservation_seats`, which the ticket
// read never touches: a database missing it could serve a ticket but not a catalogue.
func catalogueSchemaProbe(ctx context.Context, store *catalog.PostgresStore) (bool, error) {
	return store.ReadSchemaPresent(ctx)
}

// newPaymentGateway builds the payment boundary the checkout is wired with: the
// simulated provider, which never declines. A decline is a test scenario built by
// constructing an adapter with a decline rule, never the default the process runs
// with, so a real 12,13 EUR ticket is an ordinary purchase (design S8 keeps the
// provider replaceable: only this function changes for a real one).
func newPaymentGateway() payment.PaymentGateway { return payment.NewSimulated() }

// mountReservationWithProbes is mountReservation with every schema check injected.
func mountReservationWithProbes(cfg config.Config, srv *httpserver.Server, logger *slog.Logger,
	probe schemaProbe, checkoutSchema checkoutProbe, ticketSchema ticketsProbe, reads readsProbe) (*reservationProcess, error) {
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

	// payments and ticketDetails are nil interfaces unless their schema is present,
	// which is exactly what keeps each route unmounted.
	var payments publicapi.PaymentService
	if checkoutPresent {
		payments = checkout.NewService(newPaymentGateway(), checkoutStore, reservation.NewCoordinator(client), payment.ProviderName)
	} else {
		checkoutStore.Close()
		checkoutStore = nil
		logger.Info("checkout schema not present; payment route disabled", "required", "0005_checkout")
	}

	// The ticket detail route reads what the checkout wrote: the tickets and the
	// captured payment attempt they belong to. A deployment whose schema predates the
	// checkout schema has issued no tickets, so it advertises no ticket route: the
	// capability URL of a ticket does not exist there, and a route that could only
	// answer 404 or 503 is worse than an absent one. It gets its own startup budget
	// for the same reason the checkout store does.
	ticketCtx, cancelTickets := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelTickets()
	ticketStore, err := tickets.NewPostgresStore(ticketCtx, cfg.DatabaseURL)
	if err != nil {
		if checkoutStore != nil {
			checkoutStore.Close()
		}
		_ = client.Close()
		store.Close()
		return nil, fmt.Errorf("open ticket store: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}
	ticketPresent, err := ticketSchema(ticketCtx, ticketStore)
	if err != nil {
		ticketStore.Close()
		if checkoutStore != nil {
			checkoutStore.Close()
		}
		_ = client.Close()
		store.Close()
		return nil, fmt.Errorf("check the ticket schema: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}

	var ticketDetails publicapi.TicketService
	if ticketPresent {
		// The ticket surface signs the code the web page and the PDF both print, so it
		// cannot be mounted without a usable key: a ticket route that could not sign
		// would either refuse every ticket or (worse) print codes nobody can trust.
		// config.LoadFrom validates the key for a serving process; this guards a
		// programmatically built Config, the same way the hold window is guarded above.
		signer, signerErr := tickets.NewSigner(cfg.TicketQRKey)
		if signerErr != nil {
			ticketStore.Close()
			if checkoutStore != nil {
				checkoutStore.Close()
			}
			_ = client.Close()
			store.Close()
			return nil, fmt.Errorf("configure the ticket QR signing key (API_TICKET_QR_KEY): %w", signerErr)
		}
		// The service wraps the store, so the signed code belongs to the served detail:
		// the confirmation page (T7) and the PDF (T6) read it from the projection instead
		// of each minting a code of their own.
		ticketDetails = tickets.NewService(ticketStore, signer)
	} else {
		ticketStore.Close()
		ticketStore = nil
		logger.Info("ticket schema not present; ticket route disabled", "required", "0005_checkout")
	}

	// The ticket PDF is stored in the object bucket and served through a short-lived
	// pre-signed URL. The archive is built only when there are tickets to print, and the
	// download route is mounted only when it exists: a deployment without storage can
	// neither generate nor serve the document, so it must not advertise a download it
	// could not honour, while the ticket detail route keeps working.
	var ticketPDFs publicapi.TicketPDFService
	var outboxStore *pdf.OutboxStore
	var pdfWorker *pdf.Worker
	if ticketPresent {
		archive, archiveErr := newTicketArchive(cfg, ticketDetails)
		if archiveErr != nil {
			// One line, and no route: the error names the setting that is missing or
			// unusable and never a credential.
			logger.Info("object storage not configured; ticket PDF route disabled", "error", archiveErr.Error())
		} else {
			ticketPDFs = archive
			// The worker consumes the checkout's outbox row, so it needs that schema on top
			// of the tickets. Without it the download route stays mounted and generates on
			// demand, which is exactly the path a buyer needs.
			if checkoutPresent {
				outboxStore, pdfWorker, err = newTicketPDFWorker(cfg, archive, logger)
				if err != nil {
					ticketStore.Close()
					if checkoutStore != nil {
						checkoutStore.Close()
					}
					_ = client.Close()
					store.Close()
					return nil, fmt.Errorf("configure the ticket PDF worker: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
				}
			} else {
				logger.Info("checkout outbox not present; ticket PDF worker disabled", "required", "0005_checkout")
			}
		}
	}

	// The catalogue, the seat page's read of one screening and the confirmation's read are
	// the three things the public web needs and the API did not have: a stranger needs no
	// event identifier to reach the seat page, the page keeps the entry it arrived with,
	// and reloading the confirmation keeps its state. All three read what the modules
	// above wrote, so they are mounted last and only against a database that carries
	// every table they touch; one store answers all three reads, and one probe decides
	// whether any of them is advertised. It gets its own startup budget for the same
	// reason the other stores do.
	catalogCtx, cancelCatalog := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelCatalog()
	catalogStore, err := catalog.NewPostgresStore(catalogCtx, cfg.DatabaseURL)
	if err != nil {
		ticketStore.Close()
		if checkoutStore != nil {
			checkoutStore.Close()
		}
		_ = client.Close()
		store.Close()
		return nil, fmt.Errorf("open the catalogue store: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}
	readsPresent, err := reads(catalogCtx, catalogStore)
	if err != nil {
		catalogStore.Close()
		ticketStore.Close()
		if checkoutStore != nil {
			checkoutStore.Close()
		}
		_ = client.Close()
		store.Close()
		return nil, fmt.Errorf("check the catalogue schema: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}

	// catalogue and reservationReads are nil interfaces unless the schema is present,
	// which is exactly what keeps each route unmounted. One store satisfies all of them:
	// the reads are one read model, and mounting only some of them would advertise half of
	// what the web needs.
	var catalogue publicapi.CatalogService
	var reservationReads publicapi.ReservationService
	if readsPresent {
		catalogue = catalogStore
		reservationReads = catalogStore
	} else {
		catalogStore.Close()
		catalogStore = nil
		logger.Info("catalogue schema not present; catalogue and reservation read routes disabled",
			"required", "0005_checkout")
	}

	srv.WithPublic(publicapi.New(svc).WithPayments(payments).WithTickets(ticketDetails).WithTicketPDFs(ticketPDFs).
		WithCatalog(catalogue).WithReservations(reservationReads).Mux())
	logger.Info("public reservation API mounted",
		"prefix", "/api/v1", "hold_ttl", cfg.HoldTTL, "expiry_interval", cfg.ExpiryInterval,
		"expiry_batch", cfg.ExpiryBatch, "checkout", checkoutPresent, "tickets", ticketPresent,
		"ticket_pdfs", ticketPDFs != nil, "catalogue", catalogue != nil)
	if catalogue != nil {
		logger.Info("catalogue and reservation read routes mounted",
			"routes", []string{"/api/v1/events", "/api/v1/events/{eventID}", "/api/v1/reservations/{reservationId}"})
	}
	if ticketPresent {
		// One line that the ticket codes are signed, without the key: the key is a secret
		// and the fact that it is configured is what an operator needs to know. The two
		// routes below are the code's two presentations: the SVG the page shows and the
		// check a door scanner calls.
		logger.Info("ticket QR codes are signed; code presentation and verification routes mounted",
			"routes", []string{"/api/v1/tickets/{ticketId}/qr.svg", "/api/v1/tickets/verify"},
			"key_bytes", len(cfg.TicketQRKey))
	}
	if ticketPDFs != nil {
		// The bucket is not a secret, the credentials are, so only the bucket is named.
		logger.Info("ticket PDFs are stored in the object bucket; download route mounted",
			"route", "/api/v1/tickets/{ticketId}/pdf", "bucket", cfg.S3Bucket,
			"presign_ttl", cfg.S3PresignTTL, "worker", pdfWorker != nil)
	}
	return &reservationProcess{store: store, checkoutStore: checkoutStore, ticketStore: ticketStore,
		catalogStore: catalogStore, outboxStore: outboxStore, redis: client, sweeper: sweeper,
		pdfWorker: pdfWorker}, nil
}

// newTicketArchive builds the artefact archive over the S3 adapter. The adapter
// validates its configuration without any I/O, so a wrong endpoint, a missing bucket or
// empty credentials are reported before a buyer could ask for a ticket, and the
// pre-signed lifetime comes from the configuration (a zero one falls back to the
// package's documented default).
func newTicketArchive(cfg config.Config, details tickets.Reader) (*pdf.Archive, error) {
	objects, err := storage.NewS3(storage.Config{
		Endpoint:        cfg.S3Endpoint,
		Bucket:          cfg.S3Bucket,
		Region:          cfg.S3Region,
		AccessKeyID:     cfg.S3AccessKeyID,
		SecretAccessKey: cfg.S3SecretAccessKey,
	})
	if err != nil {
		return nil, err
	}
	return pdf.NewArchive(details, objects, cfg.S3PresignTTL)
}

// newTicketPDFWorker builds the eager producer of the PDFs: the worker over the
// checkout's outbox. It gets its own startup budget, because the ticket store's
// remaining deadline must not decide whether the worker starts.
func newTicketPDFWorker(cfg config.Config, archive *pdf.Archive, logger *slog.Logger) (*pdf.OutboxStore, *pdf.Worker, error) {
	startupCtx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	outbox, err := pdf.NewOutboxStore(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	worker, err := pdf.NewWorker(outbox, archive, pdf.WorkerConfig{
		Interval: pdfWorkerInterval,
		Batch:    pdfWorkerBatch,
		Timeout:  pdfWorkerTimeout,
		Logger:   logger,
	})
	if err != nil {
		outbox.Close()
		return nil, nil, err
	}
	return outbox, worker, nil
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
