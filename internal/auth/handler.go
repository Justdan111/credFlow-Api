package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Justdan111/credflow-api/pkg/response"
)

type Handler struct {
	svc        *Service
	cookie     CookieConfig
	refreshTTL time.Duration
}

func NewHandler(svc *Service, cookie CookieConfig, refreshTTL time.Duration) *Handler {
	return &Handler{svc: svc, cookie: cookie, refreshTTL: refreshTTL}
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}
	out, refresh, err := h.svc.Register(r.Context(), req, r.UserAgent())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	// Set-Cookie must be written before the status line and body.
	h.cookie.Set(w, refresh, h.refreshTTL)
	response.Success(w, http.StatusCreated, out)
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}
	out, refresh, err := h.svc.Login(r.Context(), req, r.UserAgent())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.cookie.Set(w, refresh, h.refreshTTL)
	response.Success(w, http.StatusOK, out)
}

// Me is mounted under the auth middleware, so the user ID is guaranteed
// to be in the request context.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	out, err := h.svc.Me(r.Context(), userID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

// Refresh rotates the refresh token carried by the cookie. It is deliberately
// NOT behind RequireAuth: the whole point is to work once the access token has
// expired. The cookie is the credential.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	plain, ok := ReadRefreshCookie(r)
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "missing refresh token")
		return
	}

	out, newPlain, err := h.svc.Refresh(r.Context(), plain, r.UserAgent())
	if err != nil {
		// Any refresh failure ends the session, so clear the dead cookie
		// rather than leaving the browser to retry with it forever.
		if errors.Is(err, ErrInvalidRefreshToken) || errors.Is(err, ErrRefreshReuse) {
			h.cookie.Clear(w)
		}
		writeServiceError(w, err)
		return
	}

	h.cookie.Set(w, newPlain, h.refreshTTL)
	response.Success(w, http.StatusOK, out)
}

// Logout revokes the session behind the cookie. Idempotent: a missing or
// already-revoked cookie still succeeds, so a double click cannot error.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if plain, ok := ReadRefreshCookie(r); ok {
		if err := h.svc.Logout(r.Context(), plain); err != nil {
			writeServiceError(w, err)
			return
		}
	}
	h.cookie.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		response.Fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrEmailTaken):
		response.Fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidCredentials):
		response.Fail(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrRefreshReuse):
		// Do not tell the caller reuse was detected — that is a signal to an
		// attacker. The family is already revoked either way.
		response.Fail(w, http.StatusUnauthorized, "invalid or expired refresh token")
	case errors.Is(err, ErrInvalidRefreshToken):
		response.Fail(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrUserNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	default:
		response.Fail(w, http.StatusInternalServerError, "internal server error")
	}
}
