package customers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

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

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req CreateRequest
	if !response.DecodeJSON(w, r, &req) {
		return
	}

	c, err := h.svc.Create(r.Context(), businessID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusCreated, c)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id := chi.URLParam(r, "customerId")

	c, err := h.svc.Get(r.Context(), businessID, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, c)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id := chi.URLParam(r, "customerId")

	var req UpdateRequest
	if !response.DecodeJSON(w, r, &req) {
		return
	}

	c, err := h.svc.Update(r.Context(), businessID, id, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, c)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id := chi.URLParam(r, "customerId")

	// Read first: once deleted the row is filtered out of every query, and an
	// audit entry that cannot name who was removed is of little use.
	deleted, err := h.svc.Get(r.Context(), businessID, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if err := h.svc.Delete(r.Context(), businessID, id); err != nil {
		writeServiceError(w, err)
		return
	}

	h.recorder.Record(r, audit.ActionCustomerDeleted, audit.EntityCustomer, &id,
		map[string]any{"name": deleted.Name, "riskLevel": deleted.RiskLevel})

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	q := r.URL.Query()
	listQuery := ListQuery{
		Page:      atoiOr(q.Get("page"), 1),
		PageSize:  atoiOr(q.Get("pageSize"), 20),
		Search:    q.Get("search"),
		RiskLevel: q.Get("riskLevel"),
		Sort:      q.Get("sort"),
	}

	items, total, err := h.svc.List(r.Context(), businessID, listQuery)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.SuccessWithMeta(w, http.StatusOK, items, response.Meta{
		Page:     listQuery.Page,
		PageSize: listQuery.PageSize,
		Total:    total,
	})
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrEmailTaken):
		response.Fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNoFields):
		response.Fail(w, http.StatusBadRequest, err.Error())
	default:
		response.Fail(w, http.StatusInternalServerError, "internal server error")
	}
}

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}
