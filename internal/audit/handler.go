package audit

import (
	"net/http"
	"strconv"

	"github.com/Justdan111/credflow-api/internal/auth"
	appmiddleware "github.com/Justdan111/credflow-api/internal/middleware"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	q := r.URL.Query()
	entityID := q.Get("entityId")
	// Validated here rather than left to Postgres: an unparseable id in a query
	// string would otherwise surface as a 500, the same trap ValidateUUIDParams
	// closes for path parameters.
	if entityID != "" && !appmiddleware.IsUUID(entityID) {
		response.Fail(w, http.StatusBadRequest, "entityId must be a UUID")
		return
	}
	actorID := q.Get("actorId")
	if actorID != "" && !appmiddleware.IsUUID(actorID) {
		response.Fail(w, http.StatusBadRequest, "actorId must be a UUID")
		return
	}

	listQuery := ListQuery{
		Page:       atoiOr(q.Get("page"), 1),
		PageSize:   atoiOr(q.Get("pageSize"), defaultPageSize),
		Action:     q.Get("action"),
		EntityType: q.Get("entityType"),
		EntityID:   entityID,
		ActorID:    actorID,
	}

	entries, total, err := h.svc.List(r.Context(), businessID, listQuery)
	if err != nil {
		response.Fail(w, http.StatusInternalServerError, "internal server error")
		return
	}

	response.SuccessWithMeta(w, http.StatusOK, entries, response.Meta{
		Page:     listQuery.Page,
		PageSize: listQuery.PageSize,
		Total:    total,
	})
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
