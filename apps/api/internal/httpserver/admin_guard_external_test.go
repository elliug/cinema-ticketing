package httpserver_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
)

// localAdminServer returns a fully wired Server with a guarded admin route
// (/api/v1/admin/venues) plus unguarded health routes, built through the public
// constructor and the public admin surface T3 will consume.
func localAdminServer(t *testing.T, cfg config.Config, admin http.Handler) *httpserver.Server {
	t.Helper()
	svc := health.New(health.ProcessProbe{}, health.StaticProbe{ID: "postgresql", Outcome: health.OK()})
	return httpserver.New(cfg, svc, slog.New(slog.NewTextHandler(noop{}, nil))).WithAdmin(admin)
}

func adminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/admin/venues", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// S7 — administrative routes are not available by default: with local admin
// disabled, the guarded route is unreachable while health stays intact.
func TestAdminRoutesUnavailableByDefault(t *testing.T) {
	cfg := config.Config{} // default: LocalAdminEnabled == false
	ts := httptest.NewServer(localAdminServer(t, cfg, adminMux()).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/admin/venues", "", nil)
	if err != nil {
		t.Fatalf("POST admin: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled admin status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	if resp, err := http.Get(ts.URL + "/health/live"); err != nil {
		t.Fatalf("GET /health/live: %v", err)
	} else if resp.StatusCode != http.StatusOK {
		t.Fatalf("health live status = %d, want 200 (health must stay intact)", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
}

// S8 — administrative routes cannot be enabled in a production configuration.
// The guard denies even a correct local request when the environment is not
// development.
func TestAdminDeniedInProduction(t *testing.T) {
	cfg := config.Config{Environment: "production", LocalAdminEnabled: true, LocalAdminToken: "secret"}
	ts := httptest.NewServer(localAdminServer(t, cfg, adminMux()).Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/admin/venues", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST admin: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("production admin status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// S9 — restrictions are enforced on the server, not by client-side gating: a
// remote client cannot spoof a loopback source through a forwarding header, and
// the loopback-only check is applied server-side.
func TestAdminIgnoresForwardingHeaders(t *testing.T) {
	srv := httpserver.New(config.Config{Environment: "development", LocalAdminEnabled: true, LocalAdminToken: "secret"}, nil, nil)
	h := srv.AdminHandler(adminMux())

	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/venues", nil)
	r.RemoteAddr = "192.0.2.1:54321" // real socket peer is non-loopback
	r.Header.Set("Authorization", "Bearer secret")
	// Attacker claims to be local via forwarded headers.
	r.Header.Set("X-Forwarded-For", "127.0.0.1, 10.0.0.1")
	r.Header.Set("X-Real-Ip", "127.0.0.1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("spoofed remote status = %d, want 404", w.Code)
	}
}

// S7/S8 — local administration is gated by both the enabled switch and the
// configured environment/token; only a development loopback request with the
// exact token reaches the admin handler.
func TestAdminGuardDecisionMatrix(t *testing.T) {
	inner := func(h http.Handler, r *http.Request) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	srv := httpserver.New(config.Config{Environment: "development", LocalAdminEnabled: true, LocalAdminToken: "secret"}, nil, nil)
	h := srv.AdminHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		name   string
		remote string
		token  string
		want   int
	}{
		{"missing token", "127.0.0.1:1234", "", http.StatusNotFound},
		{"wrong token", "127.0.0.1:1234", "wrong", http.StatusNotFound},
		{"non-loopback with correct token", "192.0.2.1:1234", "secret", http.StatusNotFound},
		{"ipv6 loopback authorized", "[::1]:1234", "secret", http.StatusNoContent},
		{"loopback authorized", "127.0.0.1:1234", "secret", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/venues", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("Authorization", "Bearer "+tc.token)
			if got := inner(h, r); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// S9 / secret hygiene — denial must not leak the configured token nor the
// authorization header into the response body.
func TestAdminDenialLeaksNoSecret(t *testing.T) {
	srv := httpserver.New(config.Config{Environment: "development", LocalAdminEnabled: true, LocalAdminToken: "super-secret-token"}, nil, nil)
	h := srv.AdminHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/venues", nil)
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Authorization", "Bearer super-secret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "super-secret-token") || strings.Contains(strings.ToLower(body), "bearer") {
		t.Fatalf("denial leaked a secret: %q", body)
	}
}
