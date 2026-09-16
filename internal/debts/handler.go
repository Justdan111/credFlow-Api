package debts

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

// recordDebt writes one entry, carrying the figures that make it readable later.
func (h *Handler) recordDebt(r *http.Request, action string, d Debt) {
	h.recorder.Record(r, action, audit.EntityDebt, &d.ID, map[string]any{
		"amount":     d.Amount,
		"customerId": d.CustomerID,
		"status":     d.Status,
	})
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
	if !response.DecodeJSON(w, r, &req) {
		return
	}
	d, err := h.svc.Update(r.Context(), businessID, chi.URLParam(r, "debtId"), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.recordDebt(r, audit.ActionDebtUpdated, d)
	response.Success(w, http.StatusOK, d)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	debtID := chi.URLParam(r, "debtId")

	// Read before deleting, so the entry can record what was written off.
	deleted, err := h.svc.Get(r.Context(), businessID, debtID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if err := h.svc.Delete(r.Context(), businessID, debtID); err != nil {
		writeServiceError(w, err)
		return
	}
	h.recordDebt(r, audit.ActionDebtDeleted, deleted)
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
	// Closing a debt without money changing hands is an administrative write-off
	// — exactly the kind of decision an audit trail exists to attribute.
	h.recordDebt(r, audit.ActionDebtMarkedPaid, d)
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
