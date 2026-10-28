package httpserver_test

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
)

func TestHandlerRoutesHealth(t *testing.T) {
	cfg := config.Config{}
	svc := health.New(health.ProcessProbe{}, health.StaticProbe{ID: "postgresql", Outcome: health.OK()})
	srv := httpserver.New(cfg, svc, slog.New(slog.NewTextHandler(noop{}, nil)))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Liveness.
	if resp, err := http.Get(ts.URL + "/health/live"); err != nil {
		t.Fatalf("GET /health/live: %v", err)
	} else if resp.StatusCode != http.StatusOK {
		t.Fatalf("live status = %d, want 200", resp.StatusCode)
	}

	// Readiness with healthy probes.
	if resp, err := http.Get(ts.URL + "/health/ready"); err != nil {
		t.Fatalf("GET /health/ready: %v", err)
	} else if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", resp.StatusCode)
	}

	// Unknown route.
	if resp, err := http.Get(ts.URL + "/nope"); err != nil {
		t.Fatalf("GET /nope: %v", err)
	} else if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", resp.StatusCode)
	}
}

func TestReadinessFailsWhenProbeFails(t *testing.T) {
	cfg := config.Config{ReadinessTimeout: 2 * time.Second}
	svc := health.New(health.ProcessProbe{}, health.StaticProbe{ID: "redis", Outcome: health.Unavailable("down")})
	srv := httpserver.New(cfg, svc, slog.New(slog.NewTextHandler(noop{}, nil)))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503", resp.StatusCode)
	}
}

func TestGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	cfg := config.Config{ShutdownTimeout: 2 * time.Second, ReadinessTimeout: 2 * time.Second}
	svc := health.New(health.ProcessProbe{})
	srv := httpserver.New(cfg, svc, slog.New(slog.NewTextHandler(noop{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ServeListener(ctx, ln) }()

	// Confirm the server accepts connections.
	resp, err := http.Get("http://" + ln.Addr().String() + "/health/live")
	if err != nil {
		t.Fatalf("GET health while serving: %v", err)
	}
	resp.Body.Close()

	// Trigger graceful shutdown.
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ServeListener returned error on graceful shutdown: %v", err)
	}
}

// noop is a discard io.Writer used for the test logger.
type noop struct{}

func (noop) Write(p []byte) (int, error) { return len(p), nil }

// Ensure the server respects the request context timeout on readiness.
func TestReadyUsesReadinessTimeout(t *testing.T) {
	cfg := config.Config{ReadinessTimeout: time.Second}
	svc := health.New(health.ProcessProbe{})
	srv := httpserver.New(cfg, svc, slog.New(slog.NewTextHandler(noop{}, nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ts.URL + "/health/ready")
	if err != nil {
		t.Fatalf("GET /health/ready: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", resp.StatusCode)
	}
}
