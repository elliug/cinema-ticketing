package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"log/slog"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
)

// publicSubtreeMux mirrors the reservation routes cmd/api will mount under
// /api/v1, so the test can prove the public subtree is reachable without the
// administrative guard.
func publicSubtreeMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /reservations/holds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /events/{eventID}/seats", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// publicAndAdminServer wires both subtrees on one server, which is what cmd/api
// does once the reservation surface is mounted.
func publicAndAdminServer(t *testing.T, cfg config.Config, reached *int) *httpserver.Server {
	t.Helper()
	svc := health.New(health.ProcessProbe{}, health.StaticProbe{ID: "postgresql", Outcome: health.OK()})
	return httpserver.New(cfg, svc, slog.New(slog.NewTextHandler(noop{}, nil))).
		WithAdmin(adminSubtreeMux(reached)).
		WithPublic(publicSubtreeMux())
}

// The public reservation subtree must stay reachable while the administrative
// subtree stays fail-closed: mounting both must not turn the public mount into a
// hole in the guard (S7/S9), and the more specific administrative pattern must
// keep winning.
func TestPublicSubtreeDoesNotExposeTheAdministrativeOne(t *testing.T) {
	reached := 0
	ts := httptest.NewServer(publicAndAdminServer(t, config.Config{}, &reached).Handler())
	defer ts.Close()

	public := []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/v1/reservations/holds", http.StatusCreated},
		{http.MethodGet, "/api/v1/events/evt-1/seats", http.StatusOK},
	}
	for _, probe := range public {
		resp, err := http.DefaultClient.Do(mustRequest(t, probe.method, ts.URL+probe.path))
		if err != nil {
			t.Fatalf("%s %s: %v", probe.method, probe.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != probe.want {
			t.Fatalf("%s %s status = %d, want %d", probe.method, probe.path, resp.StatusCode, probe.want)
		}
	}

	resp, err := http.DefaultClient.Do(mustRequest(t, http.MethodPost, ts.URL+"/api/v1/admin/cinemas"))
	if err != nil {
		t.Fatalf("POST /api/v1/admin/cinemas: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin route status = %d, want 404 while administration is disabled", resp.StatusCode)
	}
	if reached != 0 {
		t.Fatalf("admin subtree reached %d times while disabled, want 0", reached)
	}
}

func mustRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, url, err)
	}
	return req
}
