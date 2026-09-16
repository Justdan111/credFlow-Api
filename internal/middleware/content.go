package middleware

import (
	"mime"
	"net/http"

	"github.com/Justdan111/credflow-api/pkg/response"
)

// RequireJSON rejects a request that carries a body without declaring JSON.
//
// Bearer auth already rules out browser form CSRF; this keeps it that way. An
// endpoint that quietly accepts text/plain is what becomes exploitable the day
// somebody adds cookie auth, since a form can post text/plain but never
// application/json.
//
// Body-less verbs are exempt, as is a POST with no body — mark-paid takes none.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		header := r.Header.Get("Content-Type")
		if header == "" {
			// Handlers answer "invalid json body" themselves, which says more.
			next.ServeHTTP(w, r)
			return
		}

		// Strips parameters, so "application/json; charset=utf-8" is accepted.
		mediaType, _, err := mime.ParseMediaType(header)
		if err != nil || mediaType != "application/json" {
			response.Fail(w, http.StatusUnsupportedMediaType,
				"content-type must be application/json")
			return
		}

		next.ServeHTTP(w, r)
	})
}
