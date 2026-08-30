package middleware

import (
	"net/http"
	"strings"

	"github.com/Justdan111/credflow-api/pkg/response"
)

// corsAllowedHeaders and corsAllowedMethods are what a browser SPA needs to
// talk to this API. Kept narrow on purpose — a preflight should advertise only
// what is actually used.
const (
	corsAllowedHeaders = "Content-Type, Authorization"
	corsAllowedMethods = "GET, POST, PATCH, DELETE, OPTIONS"
	corsMaxAge         = "600" // seconds a browser may cache the preflight
)

// CORS returns middleware implementing credentialed cross-origin access.
//
// The allowlist is matched exactly and the matching origin is echoed back.
// A wildcard is never used: it is invalid alongside Allow-Credentials, and
// browsers reject the combination outright — which would silently break the
// refresh cookie the SPA depends on.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := originSet(allowedOrigins)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				// The response varies by origin, so a shared cache must not
				// serve one origin's response to another.
				w.Header().Set("Vary", "Origin")
			}

			// Preflight is answered here and never reaches the router.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
				w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				w.Header().Set("Access-Control-Max-Age", corsMaxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAllowedOrigin rejects state-changing requests that declare a foreign
// Origin. It guards /refresh and /logout as defence in depth behind
// SameSite=Strict — if the cookie policy is ever loosened, this still holds.
//
// An absent Origin is allowed: non-browser clients (curl, mobile) send none,
// and browsers always do on cross-origin requests, which is the case that
// matters.
func RequireAllowedOrigin(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := originSet(allowedOrigins)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && !allowed[origin] {
				response.Fail(w, http.StatusForbidden, "origin not allowed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originSet builds an exact-match lookup, tolerating stray whitespace and a
// trailing slash from the comma-separated ALLOWED_ORIGINS value.
func originSet(origins []string) map[string]bool {
	set := make(map[string]bool, len(origins))
	for _, o := range origins {
		o = strings.TrimSpace(o)
		o = strings.TrimSuffix(o, "/")
		if o != "" {
			set[o] = true
		}
	}
	return set
}
