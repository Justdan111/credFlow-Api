package payments

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/internal/debts"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc      *Service
	debtRepo *debts.Repository // used by CreateForDebt to look up customer_id
}

func NewHandler(svc *Service, debtRepo *debts.Repository) *Handler {
	return &Handler{svc: svc, debtRepo: debtRepo}
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
	p, replay, err := h.svc.Create(r.Context(), businessID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	// Spec-correct idempotency: 200 on replay (this already existed),
	// 201 on a fresh insert.
	if replay {
		response.Success(w, http.StatusOK, p)
		return
	}
	response.Success(w, http.StatusCreated, p)
}

// CreateForDebt serves POST /api/debts/{debtId}/payments. The debt id comes
// from the URL and we fetch it to (a) confirm it exists in the tenant, and
// (b) auto-fill the customerId on the request.
func (h *Handler) CreateForDebt(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	debtID := chi.URLParam(r, "debtId")

	debt, err := h.debtRepo.Get(r.Context(), businessID, debtID)
	if err != nil {
		if errors.Is(err, debts.ErrNotFound) {
			response.Fail(w, http.StatusNotFound, "debt not found")
			return
		}
		response.Fail(w, http.StatusInternalServerError, "internal server error")
		return
	}

	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// Trust the URL. Any customerId/debtId in the body is ignored.
	req.CustomerID = debt.CustomerID
	req.DebtID = debt.ID

	p, replay, err := h.svc.Create(r.Context(), businessID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if replay {
		response.Success(w, http.StatusOK, p)
		return
	}
	response.Success(w, http.StatusCreated, p)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	p, err := h.svc.Get(r.Context(), businessID, chi.URLParam(r, "paymentId"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, p)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := h.svc.Delete(r.Context(), businessID, chi.URLParam(r, "paymentId")); err != nil {
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

// ListByCustomer serves GET /api/customers/{customerId}/payments.
func (h *Handler) ListByCustomer(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	q := parseListQuery(r)
	q.CustomerID = chi.URLParam(r, "customerId")
	items, total, err := h.svc.List(r.Context(), businessID, q)
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
		CustomerID: q.Get("customerId"),
		DebtID:     q.Get("debtId"),
		Method:     q.Get("method"),
		Sort:       q.Get("sort"),
	}
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrCustomerNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrDebtNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
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
