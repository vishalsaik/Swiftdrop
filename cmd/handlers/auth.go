package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"

	"swiftdrop/internal/auth"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

const (
	// sessionCookie holds the sign-in token. It is HttpOnly (a script on the page,
	// including an injected one, cannot read it) and SameSite=Strict (another site
	// cannot make the browser send it).
	sessionCookie = "sd_session"
	// csrfHeader carries the session's CSRF token on every request that changes
	// something. A page on another site can neither read the token nor set the header.
	csrfHeader = "X-CSRF-Token"
)

// Accounts is what the handlers need from the auth service (*auth.Service).
type Accounts interface {
	SignUp(ctx context.Context, ip, email, name, password string) (domain.User, error)
	SignIn(ctx context.Context, ip, email, password string) (string, auth.Principal, error)
	Authenticate(ctx context.Context, token string) (auth.Principal, error)
	SignOut(ctx context.Context, p auth.Principal) error
	ChangePassword(ctx context.Context, p auth.Principal, current, next string) error
}

// errCSRF means a signed-in request that changes something did not carry the
// session's CSRF token.
var errCSRF = errors.New("missing or wrong CSRF token")

// principalKey is the context key the guard stores who a request is from under.
type principalKey struct{}

// principalFrom is who a request is from. Behind the guard it is always set; for a
// request that somehow was not, it is the zero Principal, which is no role and no
// account, so every check that compares it fails closed.
func principalFrom(r *http.Request) auth.Principal {
	p, _ := r.Context().Value(principalKey{}).(auth.Principal)
	return p
}

// identifyFromCookie is the default Server.Identify: the signed-in session named
// by the cookie, and for a request that changes something, proof that it came
// from our own page.
func (s *Server) identifyFromCookie(r *http.Request) (auth.Principal, error) {
	if s.Accounts == nil {
		return auth.Principal{}, auth.ErrUnauthenticated // no way to sign in means nobody is
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	p, err := s.Accounts.Authenticate(r.Context(), c.Value)
	if err != nil {
		return auth.Principal{}, err
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		given := r.Header.Get(csrfHeader)
		if len(given) != len(p.CSRFToken) || subtle.ConstantTimeCompare([]byte(given), []byte(p.CSRFToken)) != 1 {
			return auth.Principal{}, errCSRF
		}
	}
	return p, nil
}

// signedIn lets a request through only if someone is signed in, any role.
func (s *Server) signedIn(h http.HandlerFunc) http.HandlerFunc { return s.guard(false, h) }

// staffOnly lets a request through only if a staff member is signed in.
func (s *Server) staffOnly(h http.HandlerFunc) http.HandlerFunc { return s.guard(true, h) }

func (s *Server) guard(staffOnly bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Identify(r)
		switch {
		case err == nil:
		case errors.Is(err, auth.ErrUnauthenticated):
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		case errors.Is(err, errCSRF):
			writeError(w, http.StatusForbidden, errCSRF.Error())
			return
		default:
			log.Printf("auth error checking the session: %v", err)
			writeError(w, http.StatusInternalServerError, "Failed to check your sign-in due to an internal server error")
			return
		}
		if staffOnly && p.User.Role != domain.RoleStaff {
			writeError(w, http.StatusForbidden, "this needs a staff account")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// mayAccess reports whether the request's account may see and act on the order:
// staff any order, a customer only their own.
func mayAccess(r *http.Request, o domain.Order) bool {
	p := principalFrom(r)
	switch {
	case p.User.ID == "":
		return false
	case p.User.Role == domain.RoleStaff:
		return true
	default:
		return p.User.Role == domain.RoleCustomer && o.CustomerID == p.User.ID
	}
}

// ---------------------------------------------------------------------------
// the endpoints
// ---------------------------------------------------------------------------

// sessionBody is what signing in, signing up and "who am I" answer.
type sessionBody struct {
	User      domain.User `json:"user"`
	CSRFToken string      `json:"csrfToken"`
}

type credentials struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// SignUp makes a customer account and signs it in.
func (s *Server) SignUp(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var in credentials
	if !decodeStrict(w, r, &in) {
		return
	}
	ip := clientIP(r)
	user, err := s.Accounts.SignUp(r.Context(), ip, in.Email, in.Name, in.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	token, p, err := s.Accounts.SignIn(r.Context(), ip, in.Email, in.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusCreated, sessionBody{User: user, CSRFToken: p.CSRFToken})
}

// SignIn checks an email and password and starts a session.
func (s *Server) SignIn(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var in credentials
	if !decodeStrict(w, r, &in) {
		return
	}
	token, p, err := s.Accounts.SignIn(r.Context(), clientIP(r), in.Email, in.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, sessionBody{User: p.User, CSRFToken: p.CSRFToken})
}

// Me says who is signed in, and gives the page the CSRF token it must send back.
func (s *Server) Me(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	p := principalFrom(r)
	writeJSON(w, http.StatusOK, sessionBody{User: p.User, CSRFToken: p.CSRFToken})
}

// SignOut ends the session and clears the cookie.
func (s *Server) SignOut(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	if err := s.Accounts.SignOut(r.Context(), principalFrom(r)); err != nil {
		log.Printf("auth error signing out: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to sign out due to an internal server error")
		return
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// ChangePassword replaces the password and ends every other sign-in.
func (s *Server) ChangePassword(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if err := s.Accounts.ChangePassword(r.Context(), principalFrom(r), in.CurrentPassword, in.NewPassword); err != nil {
		writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// noStore keeps a browser or proxy from keeping an answer about who someone is.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// decodeStrict reads a JSON body into dst, refusing fields we do not know. If it
// cannot, it has already answered 400 or 413.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

// writeAuthError answers what the auth service refused. What the person did wrong
// is said; what is wrong on our side is only logged.
func writeAuthError(w http.ResponseWriter, err error) {
	var invalid *auth.InvalidError
	var many *auth.TooManyAttemptsError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "some fields are not acceptable", "fields": invalid.Fields})
	case errors.Is(err, ports.ErrEmailTaken):
		writeError(w, http.StatusConflict, "an account with that email already exists")
	case errors.Is(err, auth.ErrBadCredentials):
		writeError(w, http.StatusUnauthorized, auth.ErrBadCredentials.Error())
	case errors.Is(err, auth.ErrWrongPassword):
		writeError(w, http.StatusForbidden, auth.ErrWrongPassword.Error())
	case errors.As(err, &many):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(many.RetryAfter.Seconds()))))
		writeError(w, http.StatusTooManyRequests, many.Error())
	default:
		log.Printf("auth error: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to complete this request due to an internal server error")
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.SecureCookies || r.TLS != nil,
		MaxAge: int(auth.DefaultSessionLifetime.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.SecureCookies || r.TLS != nil,
		MaxAge: -1,
	})
}

// clientIP is the address the connection came from. Behind a proxy that is the
// proxy's address, so the per-address limits would count everyone as one: a
// deployment behind a proxy has to say so before this trusts any header.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
