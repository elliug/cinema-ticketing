package httpserver

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// localAdminAllowed is the fail-closed admission decision for a local
// administrative request. Every missing condition denies; no condition trusts a
// forwarded header. It is intentionally pure so the decision can be exercised
// deterministically.
//
// It is a defense-in-depth development guard, not production authentication:
// it never admits outside development, requires a configured token, and requires
// the real socket peer to be loopback, so a non-loopback client cannot reach
// the administrative routes by claiming any other source.
func localAdminAllowed(environment string, enabled bool, token string, remoteAddr, authHeader string) bool {
	if !enabled || environment != "development" || token == "" {
		return false
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}

	provided, valid := strings.CutPrefix(authHeader, "Bearer ")
	if !valid {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// localAdminGuard gates next behind the local-only development guard using the
// server's real socket peer. Denial is indistinguishable from "route does not
// exist" (HTTP 404), so neither the guard's presence nor the configured token
// is leaked.
func localAdminGuard(environment string, enabled bool, token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localAdminAllowed(environment, enabled, token, r.RemoteAddr, r.Header.Get("Authorization")) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
