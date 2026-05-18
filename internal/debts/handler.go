package debts

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
	d, err := h.svc.Create(r.Context(), businessID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusCreated, d)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	d, err := h.svc.Get(r.Context(), businessID, chi.URLParam(r, "debtId"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, d)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}
	d, err := h.svc.Update(r.Context(), businessID, chi.URLParam(r, "debtId"), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, d)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := h.svc.Delete(r.Context(), businessID, chi.URLParam(r, "debtId")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) MarkPaid(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	d, err := h.svc.MarkPaid(r.Context(), businessID, chi.URLParam(r, "debtId"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, d)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	q := parseListQuery(r)
	items, total, err := h.svc.List(r.Context(), businessID, q)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.SuccessWithMeta(w, http.StatusOK, items, response.Meta{
		Page: q.Page, PageSize: q.PageSize, Total: total,
	})
}

// ListByCustomer serves GET /api/customers/{customerId}/debts.
func (h *Handler) ListByCustomer(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	q := parseListQuery(r)
	items, total, err := h.svc.ListByCustomer(r.Context(), businessID, chi.URLParam(r, "customerId"), q)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.SuccessWithMeta(w, http.StatusOK, items, response.Meta{
		Page: q.Page, PageSize: q.PageSize, Total: total,
	})
}

func parseListQuery(r *http.Request) ListQuery {
	q := r.URL.Query()
	return ListQuery{
		Page:       atoiOr(q.Get("page"), 1),
		PageSize:   atoiOr(q.Get("pageSize"), 20),
		Status:     q.Get("status"),
		CustomerID: q.Get("customerId"),
		Overdue:    q.Get("overdue"),
		Sort:       q.Get("sort"),
	}
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNoFields):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrCustomerNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrAlreadyPaid):
		response.Fail(w, http.StatusConflict, err.Error())
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
