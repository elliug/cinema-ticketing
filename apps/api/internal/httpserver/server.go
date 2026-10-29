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
	"strings"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
)

// Server is the platform HTTP server.
type Server struct {
	cfg   config.Config
	svc   *health.Service
	log   *slog.Logger
	srv   *http.Server
	admin http.Handler
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

// adminPrefix is the URL prefix every administrative route is served under.
// The registered admin handler exposes routes relative to it, which is the
// shape adminapi.Mux returns.
const adminPrefix = "/api/v1/admin/"

// WithAdmin registers the administrative handler subtree that later milestones
// mount. It returns the server so callers can chain configuration. The mounted
// subtree is always placed behind AdminHandler, which is fail-closed by default,
// so an administrative route can never be exposed unguarded.
//
// admin serves routes relative to /api/v1/admin/ (for example "POST /cinemas"),
// matching adminapi.Mux; Handler strips the prefix before delegating.
func (s *Server) WithAdmin(admin http.Handler) *Server {
	s.admin = admin
	return s
}

// AdminHandler gates inner behind the local-only development guard using the
// server's configuration. It is the public boundary that administrative routes
// plug into: every request must pass the localAdminAllowed admission check
// before reaching inner.
func (s *Server) AdminHandler(inner http.Handler) http.Handler {
	return localAdminGuard(s.cfg.Environment, s.cfg.LocalAdminEnabled, s.cfg.LocalAdminToken, inner)
}

// Handler builds the HTTP handler exposing the unguarded health routes and, when
// an administrative subtree has been registered, that subtree guarded by
// AdminHandler. Health is never gated, so liveness/readiness stay reachable
// regardless of administrative configuration.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.svc.ServeLive)
	mux.HandleFunc("GET /health/ready", s.readyHandler)
	if s.admin != nil {
		mux.Handle(adminPrefix, http.StripPrefix(strings.TrimSuffix(adminPrefix, "/"), s.AdminHandler(s.admin)))
	}
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
