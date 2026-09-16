package middleware

import (
	"mime"
	"net/http"

	"github.com/Justdan111/credflow-api/pkg/response"
)

// RequireJSON rejects a request that carries a body without declaring JSON.
//
// Bearer authentication already makes this API immune to browser form CSRF: a
// cross-site form cannot set an Authorization header. The value here is keeping
// it that way. An endpoint that quietly accepts text/plain is exactly the shape
// that becomes exploitable the day somebody adds cookie authentication for
// convenience, because a plain HTML form can post text/plain but never
// application/json.
//
// GET, HEAD, DELETE and OPTIONS carry no body and are exempt. A body-less POST
// is allowed through too — POST /debts/{id}/mark-paid takes no payload, and
// demanding a content type for an empty body would be pedantry.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		header := r.Header.Get("Content-Type")
		if header == "" {
			// No body declared. Handlers that need one answer "invalid json
			// body" on their own, which is the more useful message.
			next.ServeHTTP(w, r)
			return
		}

		// ParseMediaType strips parameters, so "application/json; charset=utf-8"
		// is accepted — a common and entirely valid spelling.
		mediaType, _, err := mime.ParseMediaType(header)
		if err != nil || mediaType != "application/json" {
			response.Fail(w, http.StatusUnsupportedMediaType,
				"content-type must be application/json")
			return
		}

		next.ServeHTTP(w, r)
	})
}
