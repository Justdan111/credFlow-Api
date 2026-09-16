package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Justdan111/credflow-api/pkg/response"
)

// NotFound answers an unrouted path in the standard envelope.
//
// chi's default writes the plain text "404 page not found", which breaks a
// client that parses every response as JSON — it sees a syntax error instead of
// the reason its request failed. The whole point of a single envelope is that
// there is exactly one shape to handle.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	response.Fail(w, http.StatusNotFound, "no route matches this path")
}

// MethodNotAllowed answers a known path with the wrong verb. chi's default
// sends an empty body, which tells a client nothing at all.
func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	response.Fail(w, http.StatusMethodNotAllowed, "this method is not allowed on this path")
}

// Recoverer turns a panic into a 500 in the envelope.
//
// chi's Recoverer stops the process from dying but writes a bare status with no
// body, so a panic is indistinguishable from a network failure at the client.
// This one logs the stack for the operator and returns the request id to the
// caller, which is the only way somebody reporting "it broke" can be matched to
// the trace that explains why.
//
// Nothing about the panic itself reaches the client: the message may contain a
// query fragment, a file path, or data from another tenant.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// A client that hung up mid-response is not a server fault, and the
			// connection is already gone — there is nothing to write to.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			reqID := middleware.GetReqID(r.Context())
			log.Printf("panic [%s] %s %s: %v\n%s", reqID, r.Method, r.URL.Path, rec, debug.Stack())

			message := "internal server error"
			if reqID != "" {
				message += " (request " + reqID + ")"
			}
			response.Fail(w, http.StatusInternalServerError, message)
		}()

		next.ServeHTTP(w, r)
	})
}
