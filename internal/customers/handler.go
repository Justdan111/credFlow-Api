package customers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
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

	if err := h.svc.Delete(r.Context(), businessID, id); err != nil {
		writeServiceError(w, err)
		return
	}
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
