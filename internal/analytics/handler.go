package analytics

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.Summary(r.Context(), businessID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) RecentDebts(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.RecentDebts(r.Context(), businessID, atoiOr(r.URL.Query().Get("limit"), 0))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) RecentPayments(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.RecentPayments(r.Context(), businessID, atoiOr(r.URL.Query().Get("limit"), 0))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) RiskDistribution(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.RiskDistribution(r.Context(), businessID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) CollectionsTrend(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.CollectionsTrend(r.Context(), businessID, atoiOr(r.URL.Query().Get("months"), 0))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) CollectionRate(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.CollectionRate(r.Context(), businessID, atoiOr(r.URL.Query().Get("months"), 0))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) RiskTrend(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.RiskTrend(r.Context(), businessID, atoiOr(r.URL.Query().Get("months"), 0))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) CustomerSegments(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.CustomerSegments(r.Context(), businessID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

// Export streams the collections trend and collection rate as CSV. It writes a
// file rather than the JSON envelope, so it deliberately does not use
// response.Success.
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	// Only CSV for now. Rejecting anything else keeps the contract honest
	// instead of silently returning CSV for format=xlsx.
	if f := r.URL.Query().Get("format"); f != "" && f != "csv" {
		response.Fail(w, http.StatusBadRequest, "format must be csv")
		return
	}

	months := atoiOr(r.URL.Query().Get("months"), 0)
	trend, err := h.svc.CollectionsTrend(r.Context(), businessID, months)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rate, err := h.svc.CollectionRate(r.Context(), businessID, months)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	// Index the rate points so the two series line up by month rather than by
	// position — they are built from the same window, but relying on that
	// would be fragile.
	rateByMonth := make(map[string]CollectionRatePoint, len(rate.Points))
	for _, p := range rate.Points {
		rateByMonth[p.Month] = p
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="credflow-analytics.csv"`)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"month", "currency", "collections", "outstanding", "target", "collection_rate_pct"})
	for _, p := range trend.Points {
		rp := rateByMonth[p.Month]
		_ = cw.Write([]string{
			p.Month,
			trend.Currency,
			formatMoney(p.Collections),
			formatMoney(p.Outstanding),
			formatMoneyPtr(rp.Target),
			formatFloatPtr(rp.Rate),
		})
	}
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
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

func formatMoney(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

func formatMoneyPtr(f *float64) string {
	if f == nil {
		return ""
	}
	return formatMoney(*f)
}

func formatFloatPtr(f *float64) string {
	if f == nil {
		return ""
	}
	return fmt.Sprintf("%.1f", *f)
}
