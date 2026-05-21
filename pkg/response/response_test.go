package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSuccess_writesEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	Success(rec, http.StatusOK, map[string]string{"hello": "world"})

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type: got %q, want application/json", got)
	}

	var env struct {
		Data  map[string]string `json:"data"`
		Meta  any               `json:"meta"`
		Error any               `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data["hello"] != "world" {
		t.Errorf("data: got %v, want hello=world", env.Data)
	}
	if env.Meta != nil {
		t.Errorf("meta: got %v, want nil", env.Meta)
	}
	if env.Error != nil {
		t.Errorf("error: got %v, want nil", env.Error)
	}
}

func TestSuccess_nilDataBecomesEmptyObject(t *testing.T) {
	// The contract: list endpoints get [], detail endpoints get {}.
	// Success serves detail endpoints — nil data must render as {}, never null.
	rec := httptest.NewRecorder()
	Success(rec, http.StatusOK, nil)

	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(env.Data) != "{}" {
		t.Errorf("nil data: got %s, want {}", env.Data)
	}
}

func TestSuccessWithMeta_nilDataBecomesEmptyArray(t *testing.T) {
	// List endpoints with no rows must render data as [], not null or {}.
	rec := httptest.NewRecorder()
	SuccessWithMeta(rec, http.StatusOK, nil, Meta{Page: 1, PageSize: 20, Total: 0})

	var env struct {
		Data json.RawMessage `json:"data"`
		Meta Meta            `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(env.Data) != "[]" {
		t.Errorf("nil data: got %s, want []", env.Data)
	}
	if env.Meta.Page != 1 || env.Meta.PageSize != 20 || env.Meta.Total != 0 {
		t.Errorf("meta: got %+v", env.Meta)
	}
}

func TestFail_writesErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	Fail(rec, http.StatusBadRequest, "name is required")

	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}

	var env struct {
		Data  map[string]any `json:"data"`
		Error *ErrorDetail   `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Message != "name is required" {
		t.Errorf("error: got %+v", env.Error)
	}
	// data should be {} even on failure — keeps the envelope shape stable.
	if got := len(env.Data); got != 0 {
		t.Errorf("data on failure: got %v, want empty map", env.Data)
	}
}
