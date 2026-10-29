// Command api is the Go API platform entrypoint for the cinema ticketing
// backend.
//
// It wires configuration, structured logging, the health service, and the HTTP
// server with graceful shutdown, and pins the module boundaries into the build.
// M1 adds the local-only administrative venue API, which is mounted only when
// local administration is explicitly enabled.
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

	"cinema.local/ticketing/apps/api/internal/adminapi"
	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/log"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting api", "addr", cfg.Addr, "log_level", cfg.LogLevel)
	if err := srv.Serve(ctx); err != nil {
		logger.Error("server stopped with error", "error", err)
		return 1
	}
	logger.Info("api stopped")
	return 0
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
