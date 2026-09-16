package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func decodeEnvelope(t *testing.T, body []byte) (data json.RawMessage, message string) {
	t.Helper()
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("response is not the JSON envelope: %v (body: %s)", err, body)
	}
	if env.Error == nil {
		t.Fatalf("envelope carries no error: %s", body)
	}
	return env.Data, env.Error.Message
}

func TestNotFound_usesTheEnvelope(t *testing.T) {
	r := chi.NewRouter()
	r.NotFound(NotFound)
	r.Get("/known", func(w http.ResponseWriter, _ *http.Request) {})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
	// chi's default is the plain string "404 page not found", which a client
	// parsing every response as JSON cannot read.
	if _, msg := decodeEnvelope(t, rec.Body.Bytes()); msg == "" {
		t.Error("404 carried no message")
	}
}

func TestMethodNotAllowed_usesTheEnvelope(t *testing.T) {
	r := chi.NewRouter()
	r.MethodNotAllowed(MethodNotAllowed)
	r.Get("/known", func(w http.ResponseWriter, _ *http.Request) {})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/known", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rec.Code)
	}
	if _, msg := decodeEnvelope(t, rec.Body.Bytes()); msg == "" {
		t.Error("405 carried no message")
	}
}

func TestRecoverer_turnsPanicIntoEnvelopeWithRequestID(t *testing.T) {
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(Recoverer)
	r.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("database handle is nil")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", rec.Code)
	}

	_, msg := decodeEnvelope(t, rec.Body.Bytes())

	// The panic value may name a table, a file path, or another tenant's data.
	// None of it belongs in a response.
	if strings.Contains(msg, "database handle is nil") {
		t.Errorf("panic detail leaked to the client: %q", msg)
	}
	// The request id is the only way somebody reporting "it broke" can be
	// matched to the logged stack.
	if !strings.Contains(msg, "request ") {
		t.Errorf("message should quote the request id, got %q", msg)
	}
}

func TestRecoverer_letsAbortHandlerThrough(t *testing.T) {
	// A client that hung up mid-response is not a server fault, and the
	// connection is gone — there is nothing to write a 500 to.
	r := chi.NewRouter()
	r.Use(Recoverer)
	r.Get("/abort", func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Errorf("ErrAbortHandler should propagate, got %v", rec)
		}
	}()

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	t.Error("ErrAbortHandler was swallowed")
}
