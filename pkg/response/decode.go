package response

import (
	"encoding/json"
	"errors"
	"net/http"
)

// DecodeJSON reads a JSON body and writes the right failure itself, returning
// false when the caller should stop.
//
// It exists so every handler distinguishes an oversized body from a malformed
// one. http.MaxBytesReader, applied as middleware, surfaces the cap as a
// *http.MaxBytesError from Decode; without this check every handler would
// report a 1 MB upload as "invalid json body", which is both wrong and
// unhelpful to whoever is debugging it.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			Fail(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return false
		}
		Fail(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}
