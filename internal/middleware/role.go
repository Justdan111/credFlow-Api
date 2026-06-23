package middleware

import (
	"net/http"

	"github.com/Justdan111/credflow-api/internal/auth"
	"github.com/Justdan111/credflow-api/pkg/response"
)

// RequireRole returns middleware that allows the request through only if the
// caller's role (read from the request context) is in the allowed list.
// Must be chained AFTER RequireAuth, which is what puts the role in context.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, r := range allowed {
		set[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, ok := auth.RoleFromContext(r.Context())
			if !ok {
				// No role in context means RequireAuth didn't run — treat as
				// unauthenticated, not forbidden.
				response.Fail(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if _, allowed := set[role]; !allowed {
				response.Fail(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
