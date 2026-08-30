package middleware

import "net/http"

// SecurityHeaders sets defensive response headers.
//
// No Content-Security-Policy: this API returns JSON and never HTML, so there is
// no document for a CSP to constrain. X-Content-Type-Options and a DENY frame
// policy cover the realistic risks without pretending to more.
func SecurityHeaders(enableHSTS bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()

			// Stop a browser second-guessing our Content-Type, which is how a
			// JSON response gets treated as script.
			h.Set("X-Content-Type-Options", "nosniff")
			// The API renders nothing, so it never needs to be framed.
			h.Set("X-Frame-Options", "DENY")
			// Keep paths and query strings out of Referer on cross-origin
			// navigations — a reset link must not leak that way.
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")

			// HSTS only over TLS. Sent on plain http it would pin localhost to
			// https in the developer's browser, which is tedious to undo and
			// breaks local work. r.TLS is nil unless the connection is direct
			// TLS; behind a proxy, enableHSTS reflects the deployment.
			if enableHSTS && (r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}

			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps how much a request may send.
//
// Applied as middleware rather than per-handler so a new endpoint cannot forget
// it. Without a cap, one large request can exhaust memory before any handler
// runs — the decoder happily reads as much as it is given.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// GET and DELETE carry no body worth capping, and wrapping them
			// would only add work.
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
