// Command api is the Go API platform entrypoint for the cinema ticketing
// backend.
//
// M0 foundation only. It wires configuration, structured logging, the health
// service, and the HTTP server with graceful shutdown, and pins the module
// boundaries into the build. No domain behavior is implemented yet.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/log"

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
	cfg, err := config.Load()
	if err != nil {
		slog.Default().Error("load configuration", "error", err)
		os.Exit(1)
	}

	logger, err := log.New(cfg.LogLevel, os.Stdout)
	if err != nil {
		slog.Default().Error("init logger", "error", err)
		os.Exit(1)
	}

	probes := []health.Probe{health.ProcessProbe{}}
	for _, dependency := range cfg.Dependencies {
		probes = append(probes, health.NewTCPProbe(dependency, dependencyAddress(dependency)))
	}
	svc := health.New(probes...)

	srv := httpserver.New(cfg, svc, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting api", "addr", cfg.Addr, "log_level", cfg.LogLevel)
	if err := srv.Serve(ctx); err != nil {
		logger.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
	logger.Info("api stopped")
}

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
