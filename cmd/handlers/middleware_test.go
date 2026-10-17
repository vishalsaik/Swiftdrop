package handlers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// next records whether the middleware let the request through.
type probe struct {
	called bool
	err    error
	read   []byte
}

func (p *probe) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.called = true
		p.read, p.err = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestMaxBodyAllowsBodiesUpToTheLimit(t *testing.T) {
	p := &probe{}
	h := MaxBody(10)(p.handler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789")))

	if !p.called || p.err != nil {
		t.Fatalf("a body exactly at the limit must be readable: called=%v err=%v", p.called, p.err)
	}
	if string(p.read) != "0123456789" {
		t.Errorf("read %q", p.read)
	}
}

func TestMaxBodyFailsReadsPastTheLimit(t *testing.T) {
	p := &probe{}
	h := MaxBody(10)(p.handler())
	h.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789X")))

	var tooLarge *http.MaxBytesError
	if !errors.As(p.err, &tooLarge) {
		t.Fatalf("read error = %v, want *http.MaxBytesError", p.err)
	}
	if tooLarge.Limit != 10 {
		t.Errorf("limit = %d, want 10", tooLarge.Limit)
	}
}

func TestMaxBodyAlwaysCallsNext(t *testing.T) {
	p := &probe{}
	MaxBody(1)(p.handler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !p.called {
		t.Error("MaxBody only limits the body, it must not stop requests")
	}
}

func TestRequireJSON(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		body        string
		contentType string
		unknownLen  bool
		wantPass    bool
	}{
		{"json body", http.MethodPost, `{}`, "application/json", false, true},
		{"json with charset", http.MethodPost, `{}`, "application/json; charset=utf-8", false, true},
		{"no body, no content type", http.MethodPost, "", "", false, true},
		{"no body, odd content type", http.MethodPost, "", "text/plain", false, true},
		{"GET with no body", http.MethodGet, "", "", false, true},
		{"text body", http.MethodPost, `{}`, "text/plain", false, false},
		{"body without content type", http.MethodPost, `{}`, "", false, false},
		{"prefix lookalike", http.MethodPost, `{}`, "application/jsonp", false, false},
		{"garbage content type", http.MethodPost, `{}`, "///", false, false},
		{"unknown length without content type", http.MethodPost, `{}`, "", true, false},
		{"unknown length json", http.MethodPost, `{}`, "application/json", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &probe{}
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, "/", body)
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.unknownLen {
				req.ContentLength = -1 // e.g. chunked transfer encoding
			}
			rec := httptest.NewRecorder()
			RequireJSON(p.handler()).ServeHTTP(rec, req)

			if p.called != tt.wantPass {
				t.Fatalf("next called = %v, want %v (status %d)", p.called, tt.wantPass, rec.Code)
			}
			if !tt.wantPass {
				wantStatus(t, rec, http.StatusUnsupportedMediaType)
				wantContentTypeJSON(t, rec)
				wantErrorContains(t, rec, "application/json")
			}
		})
	}
}

// TestCORSPreflight checks the full preflight flow: CORS must answer 204
// itself and not call next, so the router never sees the OPTIONS request.
func TestCORSPreflight(t *testing.T) {
	origins := []string{"http://localhost:3000"}
	p := &probe{}
	h := CORS(origins)(p.handler())

	req := httptest.NewRequest(http.MethodOptions, "/orders", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if p.called {
		t.Error("preflight must not reach the next handler")
	}
	wantStatus(t, rec, http.StatusNoContent)
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":  "http://localhost:3000",
		"Access-Control-Allow-Methods": "GET, POST, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type",
		"Vary":                         "Origin",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// TestCORSHeaders checks per-origin behaviour for normal (non-preflight) requests.
func TestCORSHeaders(t *testing.T) {
	origins := []string{"http://localhost:3000"}
	tests := []struct {
		name       string
		origin     string
		wantHeader bool
	}{
		{"allowed origin gets header", "http://localhost:3000", true},
		{"disallowed origin gets no header", "http://evil.com", false},
		{"no origin gets no header", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &probe{}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			CORS(origins)(p.handler()).ServeHTTP(rec, req)

			if !p.called {
				t.Error("non-preflight must always reach the next handler")
			}
			// Vary: Origin is unconditional, so caches never mix up responses
			// that differ only by the Origin of the caller.
			if got := rec.Header().Get("Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want %q", got, "Origin")
			}
			got := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.wantHeader && got != tt.origin {
				t.Errorf("Allow-Origin = %q, want %q", got, tt.origin)
			}
			if !tt.wantHeader && got != "" {
				t.Errorf("Allow-Origin = %q, want empty", got)
			}
		})
	}
}

// TestCORSHeadersPresentOnErrorResponse is the proof that CORS must be the
// outermost link: even a 415 from RequireJSON must carry the CORS headers so
// the browser can read the error body. If CORS were inside Router.Use this
// test would fail.
func TestCORSHeadersPresentOnErrorResponse(t *testing.T) {
	origins := []string{"http://localhost:3000"}
	p := &probe{}
	h := CORS(origins)(RequireJSON(p.handler()))

	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{}`))
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Content-Type", "text/plain") // triggers 415
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	wantStatus(t, rec, http.StatusUnsupportedMediaType)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Allow-Origin = %q on a 415; CORS headers must be present even on error responses", got)
	}
}

// A preflight from an origin that isn't on the list gets no CORS answer at
// all: no 204 and no Allow-* headers, so the browser blocks the real request.
func TestCORSPreflightFromDisallowedOrigin(t *testing.T) {
	p := &probe{}
	h := CORS([]string{"http://localhost:3000"})(p.handler())

	req := httptest.NewRequest(http.MethodOptions, "/orders", nil)
	req.Header.Set("Origin", "http://evil.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The probe answers 204 itself, so "did CORS answer?" is p.called == false.
	if !p.called {
		t.Error("CORS must not answer a disallowed origin's preflight itself")
	}
	for _, header := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
	} {
		if got := rec.Header().Get(header); got != "" {
			t.Errorf("%s = %q, want empty", header, got)
		}
	}
}

// The wiring test: CORS has to wrap the whole router. Through Router alone a
// preflight is a 405 (mux never matches OPTIONS on a POST-only route, so no
// middleware runs); through Handler() it is answered with 204.
func TestHandlerWrapsTheWholeRouter(t *testing.T) {
	s := NewServer(newFakeDB(), testPayRouter())
	preflight := func() *http.Request {
		req := httptest.NewRequest(http.MethodOptions, "/orders", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Access-Control-Request-Method", "POST")
		return req
	}

	bare := httptest.NewRecorder()
	s.Router.ServeHTTP(bare, preflight())
	wantStatus(t, bare, http.StatusMethodNotAllowed)

	wrapped := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrapped, preflight())
	wantStatus(t, wrapped, http.StatusNoContent)
	if got := wrapped.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Allow-Origin = %q, want the request origin", got)
	}
}

// End to end through the real chain: a 415 from RequireJSON, a 422 from the
// validator and a 404 from a handler all carry the CORS header.
func TestHandlerErrorResponsesCarryCORS(t *testing.T) {
	s := NewServer(newFakeDB(), testPayRouter())
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
		want        int
	}{
		{"415 wrong content type", http.MethodPost, "/orders", `{}`, "text/plain", http.StatusUnsupportedMediaType},
		{"422 validation failure", http.MethodPost, "/orders", `{}`, "application/json", http.StatusUnprocessableEntity},
		{"404 missing order", http.MethodGet, "/orders/nope", "", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Origin", "http://localhost:5173")
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)

			wantStatus(t, rec, tt.want)
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
				t.Errorf("Allow-Origin = %q, want the request origin", got)
			}
		})
	}
}

// Links run in registration order: an earlier link that answers stops the
// later ones from ever running.
func TestChainStopsAtTheFirstAnsweringLink(t *testing.T) {
	var order []string
	link := func(name string, stop bool) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				if stop {
					w.WriteHeader(http.StatusTeapot)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}
	end := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") })

	h := link("a", false)(link("b", true)(link("c", false)(end)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := strings.Join(order, ","); got != "a,b" {
		t.Errorf("ran %q, want a,b (c and the handler must not run)", got)
	}
	wantStatus(t, rec, http.StatusTeapot)
}
