package handlers

import (
	"mime"
	"net/http"
	"slices"
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

// CORS lets browsers on the allowed origins read our responses. It must be
// the outermost link (wrap the whole router) so preflights and error
// responses are covered too.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The response depends on the Origin header for every request,
			// including ones with no or a disallowed origin. Without this a
			// shared cache could replay a header-less response to an allowed
			// origin (or the reverse), so Vary goes on before any early return.
			w.Header().Add("Vary", "Origin")

			origin := r.Header.Get("Origin")

			// 1. Read Origin. If empty or not allowed, pass through silently.
			if origin == "" || !slices.Contains(allowedOrigins, origin) {
				next.ServeHTTP(w, r)
				return
			}

			// 2. Allowed origin. Set required headers.
			w.Header().Set("Access-Control-Allow-Origin", origin)

			// 3. Preflight check.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Max-Age", "600")

				w.WriteHeader(http.StatusNoContent)
				return
			}

			// 4. Everything else. Call next with headers already set.
			next.ServeHTTP(w, r)
		})
	}
}
