// Package httpserver wires the platform HTTP transport: health routes, a
// readiness deadline, and graceful shutdown.
//
// M0 foundation only. It hosts the health endpoints and provides the server
// lifecycle; domain routes are added in later milestones.
package httpserver

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
)

// Server is the platform HTTP server.
type Server struct {
	cfg config.Config
	svc *health.Service
	log *slog.Logger
	srv *http.Server
}

// New builds a Server from configuration, a health Service, and a logger.
func New(cfg config.Config, svc *health.Service, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg: cfg,
		svc: svc,
		log: log,
		srv: &http.Server{Addr: cfg.Addr},
	}
}

// Handler builds the HTTP handler exposing the health routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.svc.ServeLive)
	mux.HandleFunc("GET /health/ready", s.readyHandler)
	return mux
}

// readyHandler applies the configured readiness deadline, then delegates to the
// health service so a stalled probe cannot hang the endpoint.
func (s *Server) readyHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ReadinessTimeout)
	defer cancel()
	s.svc.ServeReady(w, r.WithContext(ctx))
}

// Serve listens on the configured address and blocks until ctx is cancelled
// (then shuts down gracefully) or a fatal accept error occurs.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	return s.ServeListener(ctx, ln)
}

// ServeListener serves on the provided listener. When ctx is cancelled the
// server shuts down gracefully within the configured shutdown timeout.
func (s *Server) ServeListener(ctx context.Context, ln net.Listener) error {
	s.srv.Handler = s.Handler()

	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		s.log.Info("shutting down api", "timeout", s.cfg.ShutdownTimeout.String())
		if err := s.srv.Shutdown(shutCtx); err != nil {
			return err
		}
		return nil
	}
}
