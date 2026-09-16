package search

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	businessID, ok := auth.BusinessIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	results, err := h.svc.Search(r.Context(), businessID, q.Get("q"), limit)
	if err != nil {
		if errors.Is(err, ErrValidation) {
			response.Fail(w, http.StatusBadRequest, err.Error())
			return
		}
		response.Fail(w, http.StatusInternalServerError, "internal server error")
		return
	}
	response.Success(w, http.StatusOK, results)
}
