package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Justdan111/credflow-api/pkg/response"
)

// ValidateUUIDParams rejects a malformed path parameter with 400, instead of
// letting it reach Postgres and surface as a 500.
//
// Mount it on a nested Route that owns the id segment — r.Route("/{customerId}",
// ...) — not on the parent group. chi fills URL parameters only once the pattern
// carrying them has matched, so on the parent it reads an empty value and
// validates nothing.
func ValidateUUIDParams(names ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, name := range names {
				value := chi.URLParam(r, name)
				// Absent simply means this route does not carry that parameter.
				if value == "" || IsUUID(value) {
					continue
				}
				response.Fail(w, http.StatusBadRequest, name+" must be a UUID")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IsUUID reports whether s is a canonical 36-character UUID.
//
// Strict by design: Postgres also accepts braced and unhyphenated forms, but
// several spellings of one id means several URLs for one resource. The version
// and variant nibbles are not checked — ids come from the database, so this only
// has to stop a malformed value reaching the driver.
func IsUUID(s string) bool {
	const uuidLength = 36
	if len(s) != uuidLength {
		return false
	}
	for i := 0; i < uuidLength; i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		isDigit := c >= '0' && c <= '9'
		isLower := c >= 'a' && c <= 'f'
		isUpper := c >= 'A' && c <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}
