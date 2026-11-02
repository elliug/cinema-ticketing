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
// bucket. The ticket email is a second consumer of the same outbox: it is built beside the
// PDF worker when a mail provider is configured, and never blocks a sale when one is not.
// M3.5 adds the reads the public web needs: the catalogue of screenings a buyer
// can buy, one screening of that catalogue for the seat page, and the confirmation's read
// of one reservation. Identity adds the login: the route that asks for a sign-in link by
// mail and the route that spends one for a session token. It is built only where every piece
// it needs exists — a signing key, a mail provider, the migration 0010 tables — and a
// deployment without them keeps serving exactly as it did before identity existed. Beside it,
// and only where it exists, is the read that makes an account worth having: the tickets the
// signed-in account owns (GET /me/tickets), which until now the browser remembered for itself.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	// The venue timezone is an IANA name, and the ticket detail renders a screening's
	// local start with time.LoadLocation. Embedding the zone database makes that render
	// independent of the host: a container or a minimal image without tzdata would
	// otherwise refuse every venue whose zone it cannot load, and refuse to print the
	// ticket of any venue it already accepted.
	_ "time/tzdata"

	"github.com/redis/go-redis/v9"

	"cinema.local/ticketing/apps/api/internal/adminapi"
	"cinema.local/ticketing/apps/api/internal/catalog"
	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/email"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/identity"
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

	// The payment reconciler belongs to the process too: it settles the attempts a dead
	// process left pending, so their seats stop being held for nothing. Without the
	// checkout schema there is nothing to reconcile and the worker is not built.
	var reconcileStopped chan struct{}
	if reservationProcess != nil && reservationProcess.reconciler != nil {
		reconcileStopped = make(chan struct{})
		go func() {
			defer close(reconcileStopped)
			reservationProcess.reconciler.Run(ctx)
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

	// The ticket email worker belongs to the process too, and for the same reason: it
	// delivers the message of every sale the checkout recorded and stops before the
	// deferred Close releases its pool. A deployment with no mail provider starts none —
	// that is S5, and it is the state every gate runs in — so a missing mail server never
	// fails startup and never blocks a sale.
	var emailWorkerStopped chan struct{}
	if reservationProcess != nil && reservationProcess.emailWorker != nil {
		emailWorkerStopped = make(chan struct{})
		go func() {
			defer close(emailWorkerStopped)
			reservationProcess.emailWorker.Run(ctx)
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
		{"payment reconciliation", reconcileStopped},
		{"ticket PDF worker", pdfWorkerStopped},
		{"ticket email worker", emailWorkerStopped},
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

// The ticket email worker's cadence and bounds, the PDF worker's for the same reasons.
// The batch is the same twenty because one pass is a mail conversation per row: a larger
// batch would hold the pass open for longer without delivering anything sooner.
const (
	emailWorkerInterval = 5 * time.Second
	emailWorkerBatch    = 20
	emailWorkerTimeout  = 30 * time.Second
)

// expirySweepMaxBatches bounds how many batches one tick may run. The next tick
// continues from where this one stopped, so a backlog is drained across ticks
// instead of holding the worker in one long loop.
const expirySweepMaxBatches = 8

// The payment reconciler's bounds (issue #87). One tick settles at most a hundred stale
// attempts and may spend thirty seconds on them, including the provider calls, so a
// provider that stops answering leaves them pending for the next tick instead of
// wedging the worker.
const (
	reconcileBatch   = 100
	reconcileTimeout = 30 * time.Second
)

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
	emailStore    *email.PostgresStore
	identityStore *identity.PostgresStore
	// identitySessions is the pool the session read behind GET /me/tickets uses. It is a store
	// of its own because the identity package keeps the sign-in link and the session apart, and
	// one Close has to release it like every other pool the process opened.
	identitySessions *identity.PostgresSessionStore
	redis            *redis.Client
	sweeper          *reservation.Sweeper
	reconciler       *checkout.Reconciler
	pdfWorker        *pdf.Worker
	emailWorker      *email.Worker
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
	if p.emailStore != nil {
		p.emailStore.Close()
	}
	if p.identityStore != nil {
		p.identityStore.Close()
	}
	if p.identitySessions != nil {
		p.identitySessions.Close()
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
	// reconciler settles the attempts a dead process left pending (issue #87). It is
	// built only where the payment route is, because it needs the same checkout schema
	// and the same provider the route charges with.
	var reconciler *checkout.Reconciler
	if checkoutPresent {
		checkoutService := checkout.NewService(newPaymentGateway(), checkoutStore, reservation.NewCoordinator(client), payment.ProviderName)
		payments = checkoutService
		reconciler, err = checkout.NewReconciler(checkoutService, checkout.ReconcilerConfig{
			Interval: cfg.ExpiryInterval,
			// The age bound is the hold window, reused rather than configured: a pending
			// attempt older than the hold it belongs to cannot be a live provider call,
			// and a new knob would need a default nobody can justify yet.
			Age:     cfg.HoldTTL,
			Batch:   reconcileBatch,
			Timeout: reconcileTimeout,
			Logger:  logger,
		})
		if err != nil {
			checkoutStore.Close()
			_ = client.Close()
			store.Close()
			return nil, fmt.Errorf("configure the payment reconciler: %w", err)
		}
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
	// ticketRedemptions is the same store, offered as the door's admission collaborator:
	// the redemption is one guarded statement on the ticket row
	// ([tickets.PostgresStore.Redeem]), and the schema probe above already requires the
	// redemption columns, so a database that can serve a ticket can also admit it.
	var ticketRedemptions publicapi.RedemptionService
	// ticketLinks signs the link a purchase email carries. Its signer is built beside the QR
	// one and never from it: the two keys have different lifetimes, and rotating the link key
	// of an old email must not invalidate a printed code.
	var ticketLinks *tickets.LinkSigner
	// ticketSigner is the signer of the code the ticket page and the PDF print. It is kept
	// beside the service that wraps it because the account's list reads many tickets at once
	// and signs them itself, instead of asking the service for one ticket per query.
	var ticketSigner *tickets.Signer
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
		ticketSigner = signer
		// The service wraps the store, so the signed code belongs to the served detail:
		// the confirmation page (T7) and the PDF (T6) read it from the projection instead
		// of each minting a code of their own.
		ticketDetails = tickets.NewService(ticketStore, signer)
		ticketRedemptions = ticketStore

		// The link a purchase email carries is the ticket surface's second capability, and it
		// is optional here for the reason object storage is: a deployment that sends no email,
		// or was never given a link key, advertises no link route while the ticket, code and
		// admission routes keep working. The pair is validated by config.LoadFrom for a
		// serving process, so this only decides whether the route is mounted — and the line
		// names the setting, never the key.
		linkSigner, linkErr := tickets.NewLinkSigner(cfg.TicketLinkKey, cfg.TicketLinkTTL)
		if linkErr != nil {
			logger.Info("ticket link key not configured; signed ticket link route disabled",
				"required", "API_TICKET_LINK_KEY", "error", linkErr.Error())
		} else {
			ticketLinks = linkSigner
		}
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

	// The ticket email is the second consumer of the same outbox and keeps a delivery record
	// of its own (S4), so it is built next to the PDF worker and never instead of it. A
	// provider that is not configured is not a failure: the sale never depended on the mail
	// (S5), so the process says so once and keeps serving, which is the state every gate runs
	// in. The worker is also refused when the pieces it needs to send anything are missing —
	// a signed link key and the checkout outbox — because a worker that could only fail every
	// tick is noise, and one line at startup says exactly what is missing.
	var emailStore *email.PostgresStore
	var emailWorker *email.Worker
	// mailSender is the one SMTP adapter this process builds. It serves the ticket email and,
	// where the login is wired, the sign-in link: a second adapter for the same provider would
	// be a second pool and a second sender to keep in step.
	var mailSender email.Sender
	// ticketEmails is the resend action behind POST /tickets/{ticketId}/email. It is nil
	// unless the deployment can actually send, because a deployment with no mail provider
	// must not advertise a way to send mail (S5): the route is mounted from this value, so an
	// empty one advertises nothing.
	var ticketEmails publicapi.TicketEmailService
	switch {
	case !cfg.EmailEnabled:
		logger.Info("ticket email disabled: no mail provider is configured", "required", missingEmailSetting(cfg))
	case !ticketPresent:
		logger.Info("ticket schema not present; ticket email worker disabled", "required", "0005_checkout")
	case ticketLinks == nil:
		logger.Info("ticket email worker disabled: the signed ticket link key is missing", "required", "API_TICKET_LINK_KEY")
	case !checkoutPresent:
		logger.Info("checkout outbox not present; ticket email worker disabled", "required", "0005_checkout")
	default:
		// One SMTP adapter and one link builder serve both halves of the ticket email: the
		// consumer that mails every sale as it is recorded, and the resend a buyer asks for
		// from the ticket page. A second adapter would be a second pool and a second sender to
		// keep in step, and a second builder would be a second spelling of the link.
		sender, senderErr := email.NewSMTP(cfg.SMTPAddr, cfg.EmailFrom)
		if senderErr != nil {
			ticketStore.Close()
			if checkoutStore != nil {
				checkoutStore.Close()
			}
			_ = client.Close()
			store.Close()
			return nil, fmt.Errorf("configure the ticket email provider: %w", senderErr)
		}
		mailSender = sender
		link := ticketLinkBuilder(cfg.PublicWebURL, ticketLinks)
		emailStore, emailWorker, err = newTicketEmailWorker(cfg, sender, link, logger)
		if err != nil {
			ticketStore.Close()
			if checkoutStore != nil {
				checkoutStore.Close()
			}
			_ = client.Close()
			store.Close()
			return nil, fmt.Errorf("configure the ticket email worker: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
		}
		// The anti-relay counter counts in the Redis client the process already opened (the
		// one the seat holds and the checkout's release use), so a resend route costs three
		// increments an hour and no second connection pool.
		resender, resendErr := email.NewResender(sender, link, email.NewRedisResendLimiter(client))
		if resendErr != nil {
			emailStore.Close()
			ticketStore.Close()
			if checkoutStore != nil {
				checkoutStore.Close()
			}
			_ = client.Close()
			store.Close()
			return nil, fmt.Errorf("configure the ticket email resend: %w", resendErr)
		}
		ticketEmails = resender
	}

	// The login is the two routes of one act: ask for a sign-in link by mail, spend one for a
	// session token. It is built only where every piece it needs exists — the access link key,
	// a provider to mail through, and a database to open — and one line at startup names
	// whatever is missing. A deployment without identity therefore behaves exactly as it did
	// before this feature existed, which is the state every gate runs in.
	//
	// The migration 0010 tables are not probed: the identity store exposes no schema check, and
	// inventing one here would be a second place that decides what identity needs. A database
	// without them answers 503 on both routes rather than 404, which is honest — the routes are
	// open and the storage behind them is not.
	var identityStore *identity.PostgresStore
	var identityLinks publicapi.IdentityLinkService
	var identitySessions publicapi.IdentitySessionService
	// accountTickets is the read behind GET /me/tickets. It is nil unless the login is wired
	// and this database has the tickets to list, which is exactly what keeps the route
	// unmounted: a deployment with no identity has no account to answer for, and one with no
	// tickets would answer 503 to every signed-in person.
	var accountTickets publicapi.AccountTicketsService
	login, loginErr := newIdentitySurface(cfg, mailSender, client, logger)
	if loginErr != nil {
		ticketStore.Close()
		if checkoutStore != nil {
			checkoutStore.Close()
		}
		if emailStore != nil {
			emailStore.Close()
		}
		_ = client.Close()
		store.Close()
		return nil, loginErr
	}
	if login != nil {
		identityStore = login.store
		identityLinks = login.links
		identitySessions = login.sessions
		if ticketPresent && ticketSigner != nil {
			// The account's list reads what the checkout issued and signs each code with the
			// key the ticket page already prints, so the list and the page show one code per
			// ticket instead of two that could disagree.
			accountTickets = accountTicketList{store: ticketStore, signer: ticketSigner}
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
		if emailStore != nil {
			emailStore.Close()
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
		if emailStore != nil {
			emailStore.Close()
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

	publicHandler := publicapi.New(svc).WithPayments(payments).WithTickets(ticketDetails).
		WithRedemption(ticketRedemptions).WithTicketPDFs(ticketPDFs).WithTicketLinks(ticketLinks).
		WithTicketEmail(ticketEmails).WithIdentity(identityLinks, identitySessions).
		WithCatalog(catalogue).WithReservations(reservationReads).WithCancellation(svc)
	// The account's list is attached only where the login and the tickets both exist. The
	// builder itself refuses a nil half, so this is one decision in one place: no identity,
	// no route.
	if accountTickets != nil {
		publicHandler = publicHandler.WithIdentityTickets(login.authenticator, accountTickets)
	}
	srv.WithPublic(publicHandler.Mux())
	logger.Info("public reservation API mounted",
		"prefix", "/api/v1", "hold_ttl", cfg.HoldTTL, "expiry_interval", cfg.ExpiryInterval,
		"expiry_batch", cfg.ExpiryBatch, "checkout", checkoutPresent, "tickets", ticketPresent,
		"ticket_pdfs", ticketPDFs != nil, "catalogue", catalogue != nil)
	if catalogue != nil {
		logger.Info("catalogue and reservation read routes mounted",
			"routes", []string{"/api/v1/events", "/api/v1/events/{eventID}", "/api/v1/reservations/{reservationId}",
				"/api/v1/reservations/{reservationId}/cancel"})
	}
	if ticketPresent {
		// One line that the ticket codes are signed, without the key: the key is a secret
		// and the fact that it is configured is what an operator needs to know. The routes
		// below are the code's presentations and the two questions asked about it: the SVG
		// the page shows, the check a door scanner calls, and the admission that writes the
		// redemption.
		logger.Info("ticket QR codes are signed; code presentation, verification and admission routes mounted",
			"routes", []string{"/api/v1/tickets/{ticketId}/qr.svg", "/api/v1/tickets/verify",
				"/api/v1/tickets/redeem"},
			"key_bytes", len(cfg.TicketQRKey))
	}
	if ticketPDFs != nil {
		// The bucket is not a secret, the credentials are, so only the bucket is named.
		logger.Info("ticket PDFs are stored in the object bucket; download route mounted",
			"route", "/api/v1/tickets/{ticketId}/pdf", "bucket", cfg.S3Bucket,
			"presign_ttl", cfg.S3PresignTTL, "worker", pdfWorker != nil)
	}
	if emailWorker != nil {
		// The sender mailbox and the public origin are not secrets; the signed token the
		// email carries is, and it is never logged.
		logger.Info("ticket emails are delivered from the checkout outbox; worker started",
			"smtp", cfg.SMTPAddr, "from", cfg.EmailFrom, "public_web_url", cfg.PublicWebURL)
	}
	if ticketEmails != nil {
		// The bound is worth a line: it is the limit that keeps the resend from being a relay,
		// and an operator who sees it is the one who will notice if it ever moves.
		logger.Info("ticket email resend route mounted",
			"route", "/api/v1/tickets/{ticketId}/email",
			"limit", fmt.Sprintf("%d per %s per reservation", email.ResendLimit, email.ResendWindow))
	}
	if login != nil {
		// The lifetimes and the bound are configuration an operator needs to see; the key is a
		// secret and is never named here. The account's list is named only when it is mounted,
		// so the line never advertises a route this deployment does not serve.
		routes := []string{"/api/v1/identity/links", "/api/v1/identity/sessions"}
		if accountTickets != nil {
			routes = append(routes, "/api/v1/me/tickets")
		}
		logger.Info("identity login routes mounted",
			"routes", routes,
			"link_ttl", cfg.IdentityLinkTTL, "session_ttl", cfg.IdentitySessionTTL,
			"limit", fmt.Sprintf("%d per %s per address", identity.LinkLimit, identity.LinkWindow))
	}
	return &reservationProcess{store: store, checkoutStore: checkoutStore, ticketStore: ticketStore,
		catalogStore: catalogStore, outboxStore: outboxStore, emailStore: emailStore,
		identityStore: identityStore, identitySessions: loginSessionStore(login), redis: client,
		sweeper: sweeper, reconciler: reconciler, pdfWorker: pdfWorker, emailWorker: emailWorker}, nil
}

// loginSessionStore answers the session store the login was wired with, or nil when there was
// no login. It exists so the process's Close releases that pool like every other one without a
// second nil check on the surface at the call site.
func loginSessionStore(surface *identitySurface) *identity.PostgresSessionStore {
	if surface == nil {
		return nil
	}
	return surface.sessionStore
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

// newTicketEmailWorker builds the consumer of the checkout's outbox that mails the ticket.
// It mirrors the PDF worker: its own startup budget, a store that answers before the API
// starts listening, and a worker the process starts beside the others and stops with them.
//
// The provider adapter and the link builder arrive already built, because the resend route
// beside it sends the same message with the same two pieces: constructing either a second
// time would be a second sender to keep in step and a second link to keep correct.
//
// It is only called when a provider is configured (S5), so a deployment with no mail server
// never reaches the store's ping and never has a startup it could fail.
func newTicketEmailWorker(cfg config.Config, sender email.Sender, link email.LinkBuilder, logger *slog.Logger) (*email.PostgresStore, *email.Worker, error) {
	startupCtx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	store, err := email.NewPostgresStore(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	worker, err := email.NewWorker(store, sender, link, email.WorkerConfig{
		Interval: emailWorkerInterval,
		Batch:    emailWorkerBatch,
		Timeout:  emailWorkerTimeout,
		From:     cfg.EmailFrom,
		Logger:   logger,
	})
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return store, worker, nil
}

// identitySurface is what the login routes are served by: one durable store behind both
// halves of the link flow, the two domain services over it, and the session read the account
// route is gated by.
//
// The link store is one value because the two halves must read and write the same rows: an
// issuer recording nonces in one database and a redeemer spending them in another is a login
// nobody can complete, and the mistake would look like a refused link rather than like a
// miswiring. The session store is a second value because the identity package keeps the link
// and the session apart (S1, S2); it reads the same table the redeemer writes its session row
// into, so a token minted by a login is one this surface can authenticate.
type identitySurface struct {
	store    *identity.PostgresStore
	links    publicapi.IdentityLinkService
	sessions publicapi.IdentitySessionService
	// sessionStore is the pool behind [identity.SessionService]; the process closes it with
	// every other pool it opened.
	sessionStore *identity.PostgresSessionStore
	// authenticator proves the session a presented token opens. It is what GET /me/tickets
	// asks who is calling, before it reads anything.
	authenticator publicapi.SessionAuthenticator
}

// newIdentitySurface builds the login surface, or says in one line what it is missing and
// keeps serving.
//
// It is never a startup failure. A deployment that predates identity, or one that deliberately
// sends no mail, is a valid deployment: the login is an addition to a product whose guest
// checkout and ticket links do not depend on it (D4, S4), so a missing piece closes two routes
// and names itself in one informational line instead of taking the API down. Only a store that
// cannot be opened at all is an error, because that is a database failure the process cannot
// serve around.
//
// The provider is the ticket email's own adapter and the counter is the Redis client the
// process already opened: a second sender would be a second pool to keep in step, and a second
// connection for three increments a quarter of an hour buys nothing.
func newIdentitySurface(cfg config.Config, sender email.Sender, client redis.UniversalClient, logger *slog.Logger) (*identitySurface, error) {
	if strings.TrimSpace(cfg.IdentityLinkKey) == "" {
		logger.Info("identity login disabled: the signed access link key is missing",
			"required", "API_IDENTITY_LINK_KEY")
		return nil, nil
	}
	// A key that is set but unusable closes the surface rather than the process, and the line
	// names the setting instead of the key: a wrong key is still a secret.
	signer, signerErr := identity.NewAccessSigner(cfg.IdentityLinkKey, cfg.IdentityLinkTTL)
	if signerErr != nil {
		logger.Info("identity login disabled: the signed access link key is unusable",
			"required", "API_IDENTITY_LINK_KEY", "error", signerErr.Error())
		return nil, nil
	}
	if !cfg.EmailEnabled {
		logger.Info("identity login disabled: no mail provider is configured",
			"required", missingEmailSetting(cfg))
		return nil, nil
	}
	if sender == nil {
		// The login mails through the ticket email's adapter, and that one is built only where
		// the ticket email itself is wired.
		logger.Info("identity login disabled: no mail sender is wired",
			"required", "API_TICKET_LINK_KEY and 0005_checkout")
		return nil, nil
	}

	startupCtx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	store, err := identity.NewPostgresStore(startupCtx, cfg.DatabaseURL)
	if err != nil {
		// The refusal never echoes the connection string, which may carry credentials.
		return nil, fmt.Errorf("open identity store: %s", redactDatabaseURL(err.Error(), cfg.DatabaseURL))
	}

	limiter := identity.NewRedisLinkLimiter(client)
	issuer, issuerErr := identity.NewIssuer(signer, store, limiter, identityMailer{sender: sender},
		identityLinkBase(cfg.PublicWebURL), logger)
	if issuerErr != nil {
		store.Close()
		logger.Info("identity login disabled: the sign-in link could not be wired", "error", issuerErr.Error())
		return nil, nil
	}
	// The session's lifetime is its own and not the link's: a session that inherited the
	// fifteen minutes a login step lives would sign everybody out mid-page. The process logger
	// goes in with it, because the check of an address and the claim of its older orders is
	// recorded in the log and nowhere else (D5).
	redeemer, redeemerErr := identity.NewRedeemer(signer, store, cfg.IdentitySessionTTL, logger)
	if redeemerErr != nil {
		store.Close()
		logger.Info("identity login disabled: the session could not be wired", "error", redeemerErr.Error())
		return nil, nil
	}

	// The session read that the account's own list is gated by. A store that cannot be opened
	// closes the login rather than the process, like every other missing piece here: a login
	// whose sessions could be minted and never read would sign people in and then answer 503
	// for everything they signed in for.
	sessionStore, sessionStoreErr := identity.NewSessionStore(startupCtx, cfg.DatabaseURL)
	if sessionStoreErr != nil {
		store.Close()
		logger.Info("identity login disabled: the session store could not be opened",
			"error", redactDatabaseURL(sessionStoreErr.Error(), cfg.DatabaseURL))
		return nil, nil
	}
	authenticator, authenticatorErr := identity.NewSessionService(sessionStore, cfg.IdentitySessionTTL)
	if authenticatorErr != nil {
		sessionStore.Close()
		store.Close()
		logger.Info("identity login disabled: the session read could not be wired",
			"error", authenticatorErr.Error())
		return nil, nil
	}
	return &identitySurface{store: store, links: issuer, sessions: redeemer,
		sessionStore: sessionStore, authenticator: authenticator}, nil
}

// accountTicketList is the read behind GET /api/v1/me/tickets: the account's tickets from one
// query, each carrying the signed code its own ticket page serves.
//
// It signs here rather than in the store because signing is the ticket surface's job, and it
// does not go through the ticket service because that service reads one ticket per query: a
// list is the one place where reading one at a time would be a round trip per ticket. The
// result is the same code either way — the same projection, the same signer — so the web page,
// the PDF and the list show one code for one ticket.
type accountTicketList struct {
	store  *tickets.PostgresStore
	signer *tickets.Signer
}

// compile-time proof that the wiring satisfies what the account route asks of it.
var _ publicapi.AccountTicketsService = accountTicketList{}

// TicketsByCustomer answers the account's tickets with their codes, or the store's error
// unchanged: a list that could not be read is never a list with the codes missing, because a
// ticket nobody can scan is not a ticket a list may hand out.
func (l accountTicketList) TicketsByCustomer(ctx context.Context, customerID string) ([]tickets.TicketDetail, error) {
	details, err := l.store.TicketsByCustomer(ctx, customerID)
	if err != nil {
		return nil, err
	}
	for index := range details {
		token, err := l.signer.Sign(details[index])
		if err != nil {
			return nil, fmt.Errorf("%w: the ticket code could not be signed: %w", tickets.ErrUnavailable, err)
		}
		details[index].QRToken = token
	}
	return details, nil
}

// identityMailer adapts the ticket email's provider to the identity package's mail boundary.
//
// The two messages are composed by different packages and carried by one sender, so the
// adapter is the whole of the join: no second pool, no second configuration, and one place to
// change if a provider ever needs something the ticket email does not.
type identityMailer struct{ sender email.Sender }

// Send delivers one login message through the shared sender.
func (m identityMailer) Send(ctx context.Context, mail identity.Mail) error {
	return m.sender.Send(ctx, email.Message{To: mail.To, Subject: mail.Subject, Body: mail.Body})
}

// identityLinkBase is everything before the token in a login email: the public web's origin and
// the route the web serves, with exactly one slash between them.
//
// The token is appended to this string by the issuer, so the shape is decided here, once: a
// doubled slash or a missing one is a link that opens the wrong page in an inbox nobody can
// correct afterwards.
func identityLinkBase(publicWebURL string) string {
	return strings.TrimRight(publicWebURL, "/") + "/in/"
}

// ticketLinkBuilder builds the URL a ticket email carries: the public web's link route and
// the signed token of one reservation. The token is the credential the email exists to
// carry, so nothing here logs it and nothing here puts it in an error.
//
// A builder with no signer refuses instead of answering an empty link: the worker logs the
// failure and retries on the next tick, which is the difference between a message that is
// late and a message that carries a link opening nothing.
func ticketLinkBuilder(publicWebURL string, signer *tickets.LinkSigner) email.LinkBuilder {
	return func(reservationID string) (string, error) {
		if signer == nil {
			return "", errors.New("the signed ticket link key is missing (API_TICKET_LINK_KEY)")
		}
		token, err := signer.Sign(reservationID, time.Now())
		if err != nil {
			return "", err
		}
		return strings.TrimRight(publicWebURL, "/") + "/t/" + token, nil
	}
}

// missingEmailSetting names the variable that keeps the ticket email disabled, for the one
// startup line an operator reads. config.LoadFrom refuses a half-configured provider, so a
// served process reaches this with both variables empty; naming whichever is empty keeps
// the line true for a programmatically built Config too.
func missingEmailSetting(cfg config.Config) string {
	switch {
	case cfg.SMTPAddr == "" && cfg.EmailFrom == "":
		return "API_SMTP_ADDR and API_EMAIL_FROM"
	case cfg.SMTPAddr == "":
		return "API_SMTP_ADDR"
	default:
		return "API_EMAIL_FROM"
	}
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
