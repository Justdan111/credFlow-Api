package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestIsUUID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"canonical lower case", "0f696a9b-d9a7-4ce3-82fd-22442a078436", true},
		{"canonical upper case", "0F696A9B-D9A7-4CE3-82FD-22442A078436", true},
		{"mixed case", "0f696A9b-D9a7-4CE3-82fd-22442a078436", true},
		{"nil uuid", "00000000-0000-0000-0000-000000000000", true},

		{"empty", "", false},
		{"plain word", "not-a-uuid", false},
		{"numeric", "12345", false},
		// Postgres would accept these; the API deliberately does not, so one
		// resource never has several spellings of its URL.
		{"unhyphenated", "0f696a9bd9a74ce382fd22442a078436", false},
		{"braced", "{0f696a9b-d9a7-4ce3-82fd-22442a078436}", false},
		{"hyphen in the wrong place", "0f696a9bd-9a7-4ce3-82fd-22442a078436", false},
		{"one char short", "0f696a9b-d9a7-4ce3-82fd-22442a07843", false},
		{"one char long", "0f696a9b-d9a7-4ce3-82fd-22442a0784366", false},
		{"non-hex character", "0f696a9b-d9a7-4ce3-82fd-22442a07843g", false},
		// The shapes an attacker actually sends.
		{"sql fragment", "1' OR '1'='1", false},
		{"path traversal", "../../etc/passwd", false},
		{"null byte", "0f696a9b-d9a7-4ce3-82fd-22442a07843\x00", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUUID(tc.input); got != tc.want {
				t.Errorf("IsUUID(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// uuidRouter mounts the middleware the way the server does — on a nested Route
// that owns the id segment — so the test exercises chi's real param timing
// rather than a hand-built context.
func uuidRouter(t *testing.T) (*chi.Mux, *bool) {
	t.Helper()
	reached := false
	r := chi.NewRouter()
	r.Route("/customers", func(r chi.Router) {
		r.Route("/{customerId}", func(r chi.Router) {
			r.Use(ValidateUUIDParams("customerId"))
			r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
		})
	})
	return r, &reached
}

func TestValidateUUIDParams_allowsWellFormedID(t *testing.T) {
	r, reached := uuidRouter(t)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/customers/0f696a9b-d9a7-4ce3-82fd-22442a078436", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	if !*reached {
		t.Error("handler did not run for a valid UUID")
	}
}

func TestValidateUUIDParams_rejectsMalformedID(t *testing.T) {
	r, reached := uuidRouter(t)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/customers/not-a-uuid", nil))

	// 400, not 500: before this middleware the bad value reached Postgres and a
	// client mistake was reported as a server fault.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
	if *reached {
		t.Error("handler ran despite a malformed UUID")
	}

	var env struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not the JSON envelope: %v (%s)", err, rec.Body.String())
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "customerId") {
		t.Errorf("message should name the offending parameter, got %+v", env.Error)
	}
}

func TestValidateUUIDParams_ignoresParamsTheRouteDoesNotCarry(t *testing.T) {
	reached := false
	r := chi.NewRouter()
	r.Route("/customers", func(r chi.Router) {
		r.Route("/{customerId}", func(r chi.Router) {
			// noteId is named for the subtree but absent from this path. An
			// absent parameter must not be treated as a malformed one, or a
			// route that simply does not carry it would 400.
			r.Use(ValidateUUIDParams("customerId", "noteId"))
			r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})
		})
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/customers/0f696a9b-d9a7-4ce3-82fd-22442a078436", nil))

	if rec.Code != http.StatusOK || !reached {
		t.Errorf("status %d, reached %v — an absent param must not be rejected", rec.Code, reached)
	}
}

// TestValidateUUIDParams_parentGroupMountIsIneffective pins down the chi
// behaviour the production wiring depends on.
//
// If a future chi release populates parameters earlier, this test fails and
// says so — at which point the nesting is merely redundant rather than load
// bearing. Today it is load bearing: mounted on the parent, the middleware sees
// an empty string and lets everything through.
func TestValidateUUIDParams_parentGroupMountIsIneffective(t *testing.T) {
	var seen string
	r := chi.NewRouter()
	r.Route("/customers", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				seen = chi.URLParam(req, "customerId")
				next.ServeHTTP(w, req)
			})
		})
		r.Get("/{customerId}", func(w http.ResponseWriter, _ *http.Request) {})
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/customers/anything", nil))

	if seen != "" {
		t.Fatalf("chi now resolves URL params before parent-group middleware (got %q). "+
			"ValidateUUIDParams could be mounted on the parent group again.", seen)
	}
}
