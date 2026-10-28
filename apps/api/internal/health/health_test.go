package health_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"cinema.local/ticketing/apps/api/internal/health"
)

func assertStatusJSON(t *testing.T, w *httptest.ResponseRecorder, code int, raw string) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status code = %d, want %d; body=%s", w.Code, code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	got := bytes.TrimSpace(w.Body.Bytes())
	want := []byte(raw)
	if !bytes.Equal(got, want) {
		t.Fatalf("body mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestLiveHandlerReportsProcessHealth(t *testing.T) {
	svc := health.New()
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	w := httptest.NewRecorder()

	svc.ServeLive(w, req)

	assertStatusJSON(t, w, http.StatusOK, `{"status":"live"}`)
}

func TestReadyHandlerAllHealthy(t *testing.T) {
	probes := []health.Probe{
		health.ProcessProbe{},
		health.StaticProbe{ID: "postgresql", Outcome: health.OK()},
	}
	svc := health.New(probes...)
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()

	svc.ServeReady(w, req)

	assertStatusJSON(t, w, http.StatusOK,
		`{"status":"ready","checks":{"postgresql":{"status":"ok"},"process":{"status":"ok"}}}`)
}

func TestReadyHandlerWithFailingProbe(t *testing.T) {
	probes := []health.Probe{
		health.ProcessProbe{},
		health.StaticProbe{ID: "postgresql", Outcome: health.OK()},
		health.StaticProbe{ID: "redis", Outcome: health.Unavailable("not reachable")},
	}
	svc := health.New(probes...)
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()

	svc.ServeReady(w, req)

	assertStatusJSON(t, w, http.StatusServiceUnavailable,
		`{"status":"not_ready","checks":{"postgresql":{"status":"ok"},"process":{"status":"ok"},"redis":{"status":"unavailable","message":"not reachable"}},"errors":["redis: not reachable"]}`)
}

func TestReadyHandlerDegradedProbeFailsReadiness(t *testing.T) {
	probes := []health.Probe{
		health.StaticProbe{ID: "cache", Outcome: health.Degraded("slow")},
	}
	svc := health.New(probes...)
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()

	svc.ServeReady(w, req)

	assertStatusJSON(t, w, http.StatusServiceUnavailable,
		`{"status":"not_ready","checks":{"cache":{"status":"degraded","message":"slow"}},"errors":["cache: slow"]}`)
}

func TestReadyHandlerNoProbes(t *testing.T) {
	svc := health.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	w := httptest.NewRecorder()

	svc.ServeReady(w, req)

	assertStatusJSON(t, w, http.StatusOK, `{"status":"ready","checks":{}}`)
}

func TestTCPProbeReportsReachableDependency(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	probe := health.NewTCPProbe("postgresql", listener.Addr().String())
	if result := probe.Check(context.Background()); result != health.OK() {
		t.Fatalf("result = %#v, want %#v", result, health.OK())
	}
}

func TestTCPProbeReportsUnavailableDependency(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()

	probe := health.NewTCPProbe("redis", address)
	result := probe.Check(context.Background())
	if result.Status != health.ResultUnavailable || result.Message != "connection failed" {
		t.Fatalf("result = %#v, want unavailable connection failed", result)
	}
}
