package payments

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Justdan111/credflow-api/internal/audit"
	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/internal/debts"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc      *Service
	debtRepo *debts.Repository // used by CreateForDebt to look up customer_id
	// recorder writes the audit trail. Every payment route mutates money, which
	// is precisely what somebody needs to reconstruct after a discrepancy.
	recorder Recorder
}

// Recorder writes the audit trail. An interface so this package does not depend
// on how the audit service is built, and so a test can assert what was written.
type Recorder interface {
	Record(r *http.Request, action, entityType string, entityID *string, metadata map[string]any)
}

func NewHandler(svc *Service, debtRepo *debts.Repository, recorder Recorder) *Handler {
	return &Handler{svc: svc, debtRepo: debtRepo, recorder: recorder}
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
	h.recordPayment(r, audit.ActionPaymentCreated, p)
	response.Success(w, http.StatusCreated, p)
}

// recordPayment writes one money-movement entry. The amount and the linked debt
// go in the metadata: an entry saying only "a payment was updated" answers none
// of the questions asked when the books do not balance.
func (h *Handler) recordPayment(r *http.Request, action string, p Payment) {
	metadata := map[string]any{
		"amount":     p.Amount,
		"method":     p.Method,
		"customerId": p.CustomerID,
	}
	if p.DebtID != nil {
		metadata["debtId"] = *p.DebtID
	}
	h.recorder.Record(r, action, audit.EntityPayment, &p.ID, metadata)
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
	if !response.DecodeJSON(w, r, &req) {
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
	h.recordPayment(r, audit.ActionPaymentCreated, p)
	response.Success(w, http.StatusCreated, p)
}

// Update corrects a recorded payment. Restricted to owner/admin at the router:
// a correction moves money on the ledger just as recording one does.
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
	p, err := h.svc.Update(r.Context(), businessID, chi.URLParam(r, "paymentId"), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.recordPayment(r, audit.ActionPaymentUpdated, p)
	response.Success(w, http.StatusOK, p)
}

// ListByDebt serves GET /api/debts/{debtId}/payments.
func (h *Handler) ListByDebt(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	debtID := chi.URLParam(r, "debtId")

	// Confirms the debt is in this tenant, so an unknown id 404s instead of
	// returning an empty list that reads as "this debt has no payments".
	if _, err := h.debtRepo.Get(r.Context(), businessID, debtID); err != nil {
		if errors.Is(err, debts.ErrNotFound) {
			response.Fail(w, http.StatusNotFound, "debt not found")
			return
		}
		response.Fail(w, http.StatusInternalServerError, "internal server error")
		return
	}

	q := parseListQuery(r)
	q.DebtID = debtID
	items, total, err := h.svc.List(r.Context(), businessID, q)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.SuccessWithMeta(w, http.StatusOK, items, response.Meta{
		Page: q.Page, PageSize: q.PageSize, Total: total,
	})
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
	paymentID := chi.URLParam(r, "paymentId")

	// Read before voiding: afterwards the row is filtered out of every query,
	// and an entry that cannot say how much was voided is not worth writing.
	voided, err := h.svc.Get(r.Context(), businessID, paymentID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if err := h.svc.Delete(r.Context(), businessID, paymentID); err != nil {
		writeServiceError(w, err)
		return
	}
	h.recordPayment(r, audit.ActionPaymentVoided, voided)
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
