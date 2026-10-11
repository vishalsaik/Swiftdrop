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
