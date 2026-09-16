package users

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
	svc *Service
	// recorder is an interface so this package does not depend on the audit
	// service's construction, and so a test can assert what was recorded.
	recorder Recorder
}

// Recorder writes the audit trail. Team changes are exactly the kind of action
// somebody needs to reconstruct after an incident.
type Recorder interface {
	Record(r *http.Request, action, entityType string, entityID *string, metadata map[string]any)
}

func NewHandler(svc *Service, recorder Recorder) *Handler {
	return &Handler{svc: svc, recorder: recorder}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	q := r.URL.Query()
	listQuery := ListQuery{
		Page:     atoiOr(q.Get("page"), 1),
		PageSize: atoiOr(q.Get("pageSize"), defaultPageSize),
		Role:     q.Get("role"),
	}

	members, total, err := h.svc.List(r.Context(), businessID, listQuery)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.SuccessWithMeta(w, http.StatusOK, members, response.Meta{
		Page: listQuery.Page, PageSize: listQuery.PageSize, Total: total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	m, err := h.svc.Get(r.Context(), businessID, chi.URLParam(r, "userId"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, m)
}

func (h *Handler) Invite(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req InviteRequest
	if !response.DecodeJSON(w, r, &req) {
		return
	}

	// The inviter's own name goes in the email, so the recipient sees who asked
	// them to join rather than an anonymous system message.
	inviterName := "A teammate"
	if _, name, err := h.svc.ActorByID(r.Context(), actor.ID); err == nil && name != "" {
		inviterName = name
	}

	member, err := h.svc.Invite(r.Context(), actor.BusinessID, actor.ID, actor.Role, inviterName, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	h.recorder.Record(r, audit.ActionUserInvited, audit.EntityUser, &member.ID,
		map[string]any{"email": member.Email, "role": member.Role})

	response.Success(w, http.StatusCreated, InviteResponse{Member: member})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	targetID := chi.URLParam(r, "userId")

	var req UpdateRequest
	if !response.DecodeJSON(w, r, &req) {
		return
	}

	member, err := h.svc.Update(r.Context(), actor.BusinessID, actor.ID, actor.Role, targetID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	// Only a role change is worth an entry: renaming a colleague is not a
	// privilege event, and filling the trail with noise makes it useless.
	if req.Role != nil {
		h.recorder.Record(r, audit.ActionUserRoleChanged, audit.EntityUser, &member.ID,
			map[string]any{"role": member.Role, "email": member.Email})
	}

	response.Success(w, http.StatusOK, member)
}

func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFrom(r)
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	targetID := chi.URLParam(r, "userId")

	// Read before removing: afterwards the row is filtered out of every query,
	// and an audit entry that names nobody is not worth writing.
	target, err := h.svc.Get(r.Context(), actor.BusinessID, targetID)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if err := h.svc.Remove(r.Context(), actor.BusinessID, actor.ID, actor.Role, targetID); err != nil {
		writeServiceError(w, err)
		return
	}

	h.recorder.Record(r, audit.ActionUserRemoved, audit.EntityUser, &targetID,
		map[string]any{"email": target.Email, "role": target.Role})

	w.WriteHeader(http.StatusNoContent)
}

type actor struct {
	ID         string
	BusinessID string
	Role       string
}

func actorFrom(r *http.Request) (actor, bool) {
	ctx := r.Context()
	userID, okUser := auth.UserIDFromContext(ctx)
	businessID, okBusiness := auth.BusinessIDFromContext(ctx)
	role, okRole := auth.RoleFromContext(ctx)
	if !okUser || !okBusiness || !okRole {
		return actor{}, false
	}
	return actor{ID: userID, BusinessID: businessID, Role: role}, true
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrEmailTaken):
		response.Fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	// 403, not 400: these are authorization decisions about who the caller is,
	// not complaints about the shape of their request.
	case errors.Is(err, ErrEscalation):
		response.Fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrSelfTarget):
		response.Fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrLastOwner):
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
