package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalAdminGuard(t *testing.T) {
	cases := []struct {
		name, environment, token, remote, supplied string
		enabled                                    bool
		want                                       int
	}{
		{"disabled by default", "development", "secret", "127.0.0.1:1234", "secret", false, http.StatusNotFound},
		{"production denied", "production", "secret", "127.0.0.1:1234", "secret", true, http.StatusNotFound},
		{"unknown environment denied", "", "secret", "127.0.0.1:1234", "secret", true, http.StatusNotFound},
		{"remote denied", "development", "secret", "192.0.2.1:1234", "secret", true, http.StatusNotFound},
		{"ipv6 loopback authorized", "development", "secret", "[::1]:1234", "secret", true, http.StatusNoContent},
		{"malformed remote denied", "development", "secret", "127.0.0.1", "secret", true, http.StatusNotFound},
		{"wrong token denied", "development", "secret", "127.0.0.1:1234", "wrong", true, http.StatusNotFound},
		{"missing token denied", "development", "secret", "127.0.0.1:1234", "", true, http.StatusNotFound},
		{"missing configured token denied", "development", "", "127.0.0.1:1234", "", true, http.StatusNotFound},
		{"local authorized", "development", "secret", "127.0.0.1:1234", "secret", true, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := localAdminGuard(tc.environment, tc.enabled, tc.token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("Authorization", "Bearer "+tc.supplied)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == http.StatusNoContent) {
				t.Fatalf("status=%d called=%v, want status=%d", w.Code, called, tc.want)
			}
		})
	}
}
