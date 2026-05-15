package middleware

import (
	"net/http"
	"strings"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

// RequireAuth returns chi middleware that requires a valid Bearer token.
// On success it injects user_id, business_id, and role into the request
// context for downstream handlers via auth.UserIDFromContext etc.
func RequireAuth(jwt *auth.JWTService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				response.Fail(w, http.StatusUnauthorized, "missing or malformed authorization header")
				return
			}
			claims, err := jwt.Verify(token)
			if err != nil {
				response.Fail(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			ctx := auth.WithUserContext(r.Context(), claims.Subject, claims.BusinessID, claims.Role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}
