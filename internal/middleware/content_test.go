package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func jsonStack() (http.Handler, *bool) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	return RequireJSON(next), &reached
}

func TestRequireJSON(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		wantStatus  int
		wantReached bool
	}{
		{"plain json", http.MethodPost, "application/json", `{}`, http.StatusOK, true},
		// A charset parameter is valid and extremely common; rejecting it would
		// break honest clients for no gain.
		{"json with charset", http.MethodPost, "application/json; charset=utf-8", `{}`, http.StatusOK, true},
		{"json with spacing", http.MethodPatch, "application/json ; charset=UTF-8", `{}`, http.StatusOK, true},

		// The shape that matters: a browser form can post text/plain but never
		// application/json, so refusing it keeps the CSRF surface closed.
		{"text plain rejected", http.MethodPost, "text/plain", `{}`, http.StatusUnsupportedMediaType, false},
		{"form encoded rejected", http.MethodPost, "application/x-www-form-urlencoded", `a=b`, http.StatusUnsupportedMediaType, false},
		{"multipart rejected", http.MethodPost, "multipart/form-data; boundary=x", "", http.StatusUnsupportedMediaType, false},
		{"malformed header rejected", http.MethodPost, "application/", "", http.StatusUnsupportedMediaType, false},

		// No declared type: handlers answer "invalid json body" themselves,
		// which is the more useful message than a content-type complaint.
		{"absent content type passes through", http.MethodPost, "", "", http.StatusOK, true},

		// Body-less verbs are exempt.
		{"get exempt", http.MethodGet, "text/plain", "", http.StatusOK, true},
		{"delete exempt", http.MethodDelete, "text/plain", "", http.StatusOK, true},
		{"head exempt", http.MethodHead, "text/plain", "", http.StatusOK, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, reached := jsonStack()
			req := httptest.NewRequest(tc.method, "/anything", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			if *reached != tc.wantReached {
				t.Errorf("handler reached: got %v, want %v", *reached, tc.wantReached)
			}
		})
	}
}

func TestRequireJSON_rejectionUsesTheEnvelope(t *testing.T) {
	handler, _ := jsonStack()
	req := httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type: got %q, want JSON", ct)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("body is not the standard envelope: %s", rec.Body.String())
	}
}
