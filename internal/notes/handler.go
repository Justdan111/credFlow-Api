package notes

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc *Service
	// authors resolves the display name captured on each note, so attribution
	// survives the author later being removed from the team.
	authors AuthorLookup
}

// AuthorLookup returns the email and name of a user. Declared here so this
// package depends on one method rather than on the users package.
type AuthorLookup interface {
	ActorByID(ctx context.Context, userID string) (email, name string, err error)
}

func NewHandler(svc *Service, authors AuthorLookup) *Handler {
	return &Handler{svc: svc, authors: authors}
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
		Channel:  q.Get("channel"),
	}

	items, total, err := h.svc.List(r.Context(), businessID, chi.URLParam(r, "customerId"), listQuery)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.SuccessWithMeta(w, http.StatusOK, items, response.Meta{
		Page: listQuery.Page, PageSize: listQuery.PageSize, Total: total,
	})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	businessID, okBusiness := auth.BusinessIDFromContext(ctx)
	userID, okUser := auth.UserIDFromContext(ctx)
	if !okBusiness || !okUser {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req CreateRequest
	if !response.DecodeJSON(w, r, &req) {
		return
	}

	authorName := "A teammate"
	if _, name, err := h.authors.ActorByID(ctx, userID); err == nil && name != "" {
		authorName = name
	}

	note, err := h.svc.Create(ctx, businessID, chi.URLParam(r, "customerId"), userID, authorName, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusCreated, note)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err := h.svc.Delete(r.Context(), businessID, chi.URLParam(r, "noteId")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrCustomerNotFound):
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
