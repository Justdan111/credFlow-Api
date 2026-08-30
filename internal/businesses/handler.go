package businesses

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

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
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
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
		response.Success(w, http.StatusOK, out)
		return
	}

	out, err := h.svc.Update(r.Context(), businessID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
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
