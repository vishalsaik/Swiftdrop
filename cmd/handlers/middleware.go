package handlers

import (
	"mime"
	"net/http"
)

// Each function here is one link in the HTTP middleware chain: it either
// answers the request itself and stops, or calls next. NewServer registers
// them with Router.Use, and they run in registration order.

// MaxBody caps how many bytes a handler can read from the request body.
// Reading past the cap fails with *http.MaxBytesError, which the handler
// that reads the body turns into a 413.
func MaxBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// RequireJSON rejects requests that carry a body that isn't application/json
// with a 415. Requests with no body (GETs, and the lifecycle POSTs like /pay)
// pass straight through. A ContentLength of -1 means "unknown", which still
// counts as having a body.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 0 {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
