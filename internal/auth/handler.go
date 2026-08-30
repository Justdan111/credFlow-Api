package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

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

// UpdateMe changes the caller's own profile.
func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}
	out, err := h.svc.UpdateProfile(r.Context(), userID, req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}

	// The refresh cookie identifies the caller's own session so it can be
	// spared while every other one is revoked.
	currentRefresh, _ := ReadRefreshCookie(r)

	if err := h.svc.ChangePassword(r.Context(), userID, req, currentRefresh); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	// The refresh cookie is scoped to /api/auth, so the browser sends it here
	// and the current session can be flagged without extra plumbing.
	currentRefresh, _ := ReadRefreshCookie(r)

	out, err := h.svc.ListSessions(r.Context(), userID, currentRefresh)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	response.Success(w, http.StatusOK, out)
}

func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserIDFromContext(r.Context())
	if !ok {
		response.Fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		response.Fail(w, http.StatusBadRequest, "sessionId is required")
		return
	}
	if err := h.svc.RevokeSession(r.Context(), userID, sessionID); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ForgotPassword always answers 202, whether or not the address exists. Any
// difference in status or body would make this an account-enumeration oracle.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := h.svc.ForgotPassword(r.Context(), req); err != nil {
		// Even an internal failure must not reveal whether the account exists.
		response.Success(w, http.StatusAccepted, map[string]string{
			"message": "if that email is registered, a reset link has been sent",
		})
		return
	}
	response.Success(w, http.StatusAccepted, map[string]string{
		"message": "if that email is registered, a reset link has been sent",
	})
}

func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Fail(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := h.svc.ResetPassword(r.Context(), req); err != nil {
		writeServiceError(w, err)
		return
	}
	// The client must sign in again: every session was just revoked.
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
	case errors.Is(err, ErrPasswordMismatch):
		response.Fail(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrSessionNotFound):
		response.Fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrResetTokenInvalid):
		// One message for expired, used and unknown alike, so the response
		// cannot be used to probe which tokens exist.
		response.Fail(w, http.StatusBadRequest, err.Error())
	default:
		response.Fail(w, http.StatusInternalServerError, "internal server error")
	}
}
