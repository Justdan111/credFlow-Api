package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Justdan111/credflow-api/pkg/response"
)

// NotFound answers an unrouted path in the standard envelope. chi's default
// writes plain text, which breaks a client that parses every response as JSON.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	response.Fail(w, http.StatusNotFound, "no route matches this path")
}

// MethodNotAllowed answers a known path with the wrong verb. chi's default
// sends an empty body.
func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	response.Fail(w, http.StatusMethodNotAllowed, "this method is not allowed on this path")
}

// Recoverer turns a panic into a 500 in the envelope, logging the stack and
// returning the request id so a bug report can be matched to it.
//
// Nothing about the panic itself reaches the client: the value may contain a
// query fragment, a file path, or another tenant's data.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// The client hung up; there is nothing left to write to.
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
