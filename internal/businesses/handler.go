package businesses

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Justdan111/credflow-api/internal/audit"
	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc      *Service
	recorder Recorder
}

// Recorder writes the audit trail. Declared as an interface here so this
// package does not depend on how the audit service is constructed.
type Recorder interface {
	Record(r *http.Request, action, entityType string, entityID *string, metadata map[string]any)
}

func NewHandler(svc *Service, recorder Recorder) *Handler {
	return &Handler{svc: svc, recorder: recorder}
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.Get(r.Context(), businessID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

// Update is mounted behind a role gate: editing the business profile is an
// administrative action, matching the policy applied to destructive endpoints.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	// Decode into a raw map first so an explicit null can be told apart from an
	// omitted key — the target field needs that distinction to be clearable.
	var raw map[string]json.RawMessage
	if !response.DecodeJSON(w, r, &raw) {
		return
	}

	var req UpdateRequest
	buf, _ := json.Marshal(raw)
	if err := json.Unmarshal(buf, &req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}

	// An explicit "monthlyCollectionTarget": null clears the target.
	if v, present := raw["monthlyCollectionTarget"]; present && string(v) == "null" {
		out, err := h.svc.ClearTarget(r.Context(), businessID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		h.recorder.Record(r, audit.ActionBusinessUpdated, audit.EntityBusiness, &businessID,
			map[string]any{"monthlyCollectionTarget": nil})
		response.Success(w, http.StatusOK, out)
		return
	}

	out, err := h.svc.Update(r.Context(), businessID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	// The currency and collection target shape every figure this business
	// reports, so a change to either needs to be attributable.
	h.recorder.Record(r, audit.ActionBusinessUpdated, audit.EntityBusiness, &businessID,
		map[string]any{"currency": out.Currency, "monthlyCollectionTarget": out.MonthlyCollectionTarget})
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) OnboardingStatus(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.OnboardingStatus(r.Context(), businessID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) OnboardingComplete(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req CompleteRequest
	if !response.DecodeJSON(w, r, &req) {
		return
	}

	out, err := h.svc.Complete(r.Context(), businessID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusCreated, out)
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrCurrencyLocked):
		response.Fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrAlreadyOnboarded):
		response.Fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	default:
		response.Fail(w, http.StatusInternalServerError, "internal server error")
	}
}
