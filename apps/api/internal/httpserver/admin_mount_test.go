package httpserver_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
)

// adminSubtreeMux mirrors the shape cmd/api mounts: adminapi.Mux() returns
// relative routes that the server exposes under /api/v1/admin/.
func adminSubtreeMux(reached *int) http.Handler {
	mux := http.NewServeMux()
	hit := func(w http.ResponseWriter, r *http.Request) {
		if reached != nil {
			*reached++
		}
		w.WriteHeader(http.StatusCreated)
	}
	mux.HandleFunc("POST /cinemas", hit)
	mux.HandleFunc("GET /layouts/{layoutId}", hit)
	return mux
}

// mountedServer wires a server exactly as cmd/api does, with a stub admin
// subtree instead of the PostgreSQL-backed handler.
func mountedServer(t *testing.T, cfg config.Config, reached *int) *httpserver.Server {
	t.Helper()
	svc := health.New(health.ProcessProbe{}, health.StaticProbe{ID: "postgresql", Outcome: health.OK()})
	return httpserver.New(cfg, svc, slog.New(slog.NewTextHandler(noop{}, nil))).WithAdmin(adminSubtreeMux(reached))
}

// S7/S8 — a disabled or production configuration exposes no route under
// /api/v1/admin/ while health keeps succeeding. The relative admin route table
// must not become reachable merely because it was mounted.
func TestAdminSubtreeUnreachableWhenNotAdmitted(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
	}{
		{
			name: "disabled by default",
			cfg:  config.Config{},
		},
		{
			name: "production with token and database url",
			cfg: config.Config{
				Environment:       "production",
				LocalAdminEnabled: true,
				LocalAdminToken:   "secret",
				DatabaseURL:       "postgres://cinema@127.0.0.1:5432/cinema?sslmode=disable",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := 0
			ts := httptest.NewServer(mountedServer(t, tc.cfg, &reached).Handler())
			defer ts.Close()

			for _, probe := range []struct {
				method, path string
			}{
				{http.MethodPost, "/api/v1/admin/cinemas"},
				{http.MethodGet, "/api/v1/admin/layouts/layout-1"},
			} {
				req, err := http.NewRequest(probe.method, ts.URL+probe.path, nil)
				if err != nil {
					t.Fatalf("build request %s %s: %v", probe.method, probe.path, err)
				}
				req.Header.Set("Authorization", "Bearer secret")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", probe.method, probe.path, err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("%s %s status = %d, want 404", probe.method, probe.path, resp.StatusCode)
				}
			}

			if reached != 0 {
				t.Fatalf("admin subtree reached %d times, want 0", reached)
			}

			resp, err := http.Get(ts.URL + "/health/live")
			if err != nil {
				t.Fatalf("GET /health/live: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("health live status = %d, want 200", resp.StatusCode)
			}
		})
	}
}

// S7 — a request reaches the mounted admin subtree only when the guard admits
// it: the development loopback request carrying the exact token is forwarded
// under the /api/v1/admin/ prefix, while missing or wrong credentials never
// enter the subtree.
func TestAdminSubtreeReachedOnlyWhenAdmitted(t *testing.T) {
	cfg := config.Config{
		Environment:       "development",
		LocalAdminEnabled: true,
		LocalAdminToken:   "secret",
		DatabaseURL:       "postgres://cinema@127.0.0.1:5432/cinema?sslmode=disable",
	}
	reached := 0
	ts := httptest.NewServer(mountedServer(t, cfg, &reached).Handler())
	defer ts.Close()

	cases := []struct {
		name       string
		authHeader string
		wantStatus int
		wantHit    bool
	}{
		{"missing authorization denied", "", http.StatusNotFound, false},
		{"wrong token denied", "Bearer wrong", http.StatusNotFound, false},
		{"admitting request forwarded under the mounted prefix", "Bearer secret", http.StatusCreated, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := reached
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/admin/cinemas", nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /api/v1/admin/cinemas: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if got := reached - before; (got == 1) != tc.wantHit {
				t.Fatalf("admin subtree hits = %d, want hit = %v", got, tc.wantHit)
			}
		})
	}
}
