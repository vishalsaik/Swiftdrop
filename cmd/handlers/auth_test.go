package handlers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/auth"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Signing up and in, the cookie, CSRF, and who may call what. These servers use
// the real auth service (with cheap hashing) on the fake store and NO Identify
// override, so a request is who its cookie says.
// ---------------------------------------------------------------------------

const password = "a long enough phrase"

var ctxBackground = context.Background()

// withPrincipal is a context that says who a request is from, as the guard would.
func withPrincipal(r *http.Request, p auth.Principal) context.Context {
	return context.WithValue(r.Context(), principalKey{}, p)
}

var cheapHashing = auth.Params{Time: 1, MemoryKiB: 64, Threads: 1, KeyLen: 16, SaltLen: 8}

type world struct {
	db  *fakeDB
	svc *auth.Service
	s   *Server
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db := newFakeDB()
	svc := auth.New(db)
	svc.Params = cheapHashing
	s := NewServer(db, testPayRouter())
	s.Accounts = svc
	return &world{db: db, svc: svc, s: s}
}

// browser is a person's browser: it keeps the cookie and the CSRF token and sends
// them back the way the dashboard does.
type browser struct {
	t       *testing.T
	s       *Server
	ip      string
	cookie  *http.Cookie
	csrf    string
	noCSRF  bool // leave the CSRF header off, to see what is refused
	userID  string
	lastRec *httptest.ResponseRecorder
}

func (w *world) browser(t *testing.T) *browser {
	return &browser{t: t, s: w.s, ip: "198.51.100.7:4000"}
}

func (b *browser) do(method, path, body string) *httptest.ResponseRecorder {
	b.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = b.ip
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	if b.csrf != "" && !b.noCSRF {
		req.Header.Set(csrfHeader, b.csrf)
	}
	b.lastRec = serve(b.s, req)
	return b.lastRec
}

// takeSession keeps the cookie and token a sign-in or sign-up answered with.
func (b *browser) takeSession(rec *httptest.ResponseRecorder) {
	b.t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			b.cookie = c
		}
	}
	var body sessionBody
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&body); err == nil {
		b.csrf, b.userID = body.CSRFToken, body.User.ID
	}
}

func (b *browser) signUp(email string) *httptest.ResponseRecorder {
	b.t.Helper()
	rec := b.do(http.MethodPost, "/auth/signup", `{"email":"`+email+`","name":"Ada Lovelace","password":"`+password+`"}`)
	wantStatus(b.t, rec, http.StatusCreated)
	b.takeSession(rec)
	return rec
}

func (b *browser) signIn(email, pw string) *httptest.ResponseRecorder {
	b.t.Helper()
	rec := b.do(http.MethodPost, "/auth/login", `{"email":"`+email+`","password":"`+pw+`"}`)
	if rec.Code == http.StatusOK {
		b.takeSession(rec)
	}
	return rec
}

func (w *world) staff(t *testing.T, email string) *browser {
	t.Helper()
	if _, err := w.svc.CreateStaff(ctxBackground, email, "Dee Patch", password); err != nil {
		t.Fatal(err)
	}
	b := w.browser(t)
	wantStatus(t, b.signIn(email, password), http.StatusOK)
	return b
}

func (w *world) customer(t *testing.T, email string) *browser {
	t.Helper()
	b := w.browser(t)
	b.signUp(email)
	return b
}

// ---------------------------------------------------------------------------
// signing up
// ---------------------------------------------------------------------------

func TestSigningUpMakesACustomerSignsThemInAndSetsASafeCookie(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t)
	rec := b.signUp("ada@example.com")

	var body sessionBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.User.Email != "ada@example.com" || body.User.Role != domain.RoleCustomer || body.User.ID == "" || len(body.CSRFToken) < 20 {
		t.Errorf("body = %+v", body)
	}
	for _, leak := range []string{password, "hash", "argon2"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(leak)) {
			t.Errorf("the answer contains %q: %s", leak, rec.Body)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: an answer about who someone is must not be kept", got)
	}
	c := b.cookie
	if c == nil || c.Name != "sd_session" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge <= 0 || c.Secure {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict, a path and a lifetime (and not Secure over plain HTTP)", c)
	}
	if c.MaxAge != int(auth.DefaultSessionLifetime.Seconds()) {
		t.Errorf("cookie lifetime = %ds, want the session's 14 days", c.MaxAge)
	}
	wantStatus(t, b.do(http.MethodGet, "/auth/me", ""), http.StatusOK)
}

func TestTheCookieIsSecureOverTLSAndWhenToldSo(t *testing.T) {
	w := newWorld(t)
	body := `{"email":"ada@example.com","name":"Ada","password":"` + password + `"}`

	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{}
	rec := serve(w.s, req)
	if c := cookieNamed(rec, sessionCookie); c == nil || !c.Secure {
		t.Errorf("over TLS the cookie = %+v, want Secure", c)
	}

	w.s.SecureCookies = true // TLS is ended by a proxy
	b := w.browser(t)
	rec = b.do(http.MethodPost, "/auth/login", `{"email":"ada@example.com","password":"`+password+`"}`)
	if c := cookieNamed(rec, sessionCookie); c == nil || !c.Secure {
		t.Errorf("with SecureCookies the cookie = %+v, want Secure", c)
	}
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestSigningUpNamesWhatIsWrongAndRefusesATakenEmail(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t)
	rec := b.do(http.MethodPost, "/auth/signup", `{"email":"nope","name":"","password":"short"}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	for _, f := range []string{"email", "name", "password"} {
		if body.Fields[f] == "" {
			t.Errorf("field %q not named: %s", f, rec.Body)
		}
	}
	if len(w.db.Users) != 0 {
		t.Error("an account was made from invalid input")
	}

	w.customer(t, "ada@example.com")
	rec = b.do(http.MethodPost, "/auth/signup", `{"email":"ADA@example.com","name":"Other","password":"`+password+`"}`)
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "already exists")
}

func TestTooManySignUpsFromOneAddressAre429WithAWait(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t)
	for i := range 10 {
		wantStatus(t, b.do(http.MethodPost, "/auth/signup", `{"email":"u`+strconv.Itoa(i)+`@example.com","name":"U","password":"`+password+`"}`), http.StatusCreated)
	}
	rec := b.do(http.MethodPost, "/auth/signup", `{"email":"more@example.com","name":"U","password":"`+password+`"}`)
	wantStatus(t, rec, http.StatusTooManyRequests)
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 3600 {
		t.Errorf("Retry-After = %q, want a number of seconds up to an hour", rec.Header().Get("Retry-After"))
	}
}

func TestABodyThatIsNotAcceptableIsRefusedBeforeAnythingIsDone(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t)
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"not JSON":      {`{`, http.StatusBadRequest},
		"empty":         {`  `, http.StatusBadRequest},
		"unknown field": {`{"email":"a@b.co","name":"A","password":"` + password + `","role":"staff"}`, http.StatusBadRequest},
		"too large":     {`{"email":"` + strings.Repeat("a", 2<<20) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		rec := b.do(http.MethodPost, "/auth/signup", c.body)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, c.want, rec.Body)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/signup", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	wantStatus(t, serve(w.s, req), http.StatusUnsupportedMediaType)
	if len(w.db.Users) != 0 {
		t.Error("an account was made")
	}
}

// Nobody can make themselves staff through the web: the role is not a field.
func TestAWebSignUpCannotAskForTheStaffRole(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t)
	wantStatus(t, b.do(http.MethodPost, "/auth/signup", `{"email":"a@b.co","name":"A","password":"`+password+`","role":"staff"}`), http.StatusBadRequest)
	b.signUp("c@d.co")
	if u := w.db.Users["c@d.co"].User; u.Role != domain.RoleCustomer {
		t.Errorf("role = %s", u.Role)
	}
}

// ---------------------------------------------------------------------------
// signing in and out
// ---------------------------------------------------------------------------

func TestAWrongPasswordAndAnUnknownEmailAreAnswerTheSameWay(t *testing.T) {
	w := newWorld(t)
	w.customer(t, "ada@example.com")
	b := w.browser(t)
	wrong := b.signIn("ada@example.com", "not the password at all")
	unknown := b.signIn("nobody@example.com", password)
	wantStatus(t, wrong, http.StatusUnauthorized)
	wantStatus(t, unknown, http.StatusUnauthorized)
	if wrong.Body.String() != unknown.Body.String() {
		t.Errorf("answers differ: %q vs %q, so one tells which emails have accounts", wrong.Body, unknown.Body)
	}
	if cookieNamed(wrong, sessionCookie) != nil || cookieNamed(unknown, sessionCookie) != nil {
		t.Error("a failed sign-in set a session cookie")
	}
}

func TestFiveWrongPasswordsLockTheAccountWithAWait(t *testing.T) {
	w := newWorld(t)
	w.customer(t, "ada@example.com")
	b := w.browser(t)
	for range 5 {
		wantStatus(t, b.signIn("ada@example.com", "wrong password here"), http.StatusUnauthorized)
	}
	rec := b.signIn("ada@example.com", password)
	wantStatus(t, rec, http.StatusTooManyRequests)
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 800 || secs > 900 {
		t.Errorf("Retry-After = %q, want about 15 minutes", rec.Header().Get("Retry-After"))
	}
}

func TestWhoAmIAnswersWithTheCSRFTokenAndNeedsASignIn(t *testing.T) {
	w := newWorld(t)
	b := w.customer(t, "ada@example.com")
	rec := b.do(http.MethodGet, "/auth/me", "")
	var body sessionBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusOK || body.User.Email != "ada@example.com" || body.CSRFToken != b.csrf {
		t.Errorf("me = %d %+v", rec.Code, body)
	}

	anonymous := w.browser(t)
	wantStatus(t, anonymous.do(http.MethodGet, "/auth/me", ""), http.StatusUnauthorized)
	anonymous.cookie = &http.Cookie{Name: sessionCookie, Value: strings.Repeat("x", 43)}
	wantStatus(t, anonymous.do(http.MethodGet, "/auth/me", ""), http.StatusUnauthorized)
	anonymous.cookie = &http.Cookie{Name: sessionCookie, Value: "short"}
	wantStatus(t, anonymous.do(http.MethodGet, "/auth/me", ""), http.StatusUnauthorized)
}

func TestSigningOutEndsTheSessionAndClearsTheCookie(t *testing.T) {
	w := newWorld(t)
	b := w.customer(t, "ada@example.com")
	oldCookie := b.cookie

	rec := b.do(http.MethodPost, "/auth/logout", "")
	wantStatus(t, rec, http.StatusNoContent)
	if c := cookieNamed(rec, sessionCookie); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("cookie after signing out = %+v, want it cleared", c)
	}
	// The cleared cookie has the attributes of the one it replaces: a browser will not
	// let a cookie that is not Secure replace one that is.
	if c := cookieNamed(rec, sessionCookie); !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cleared cookie = %+v, want HttpOnly, SameSite=Strict and the same path", c)
	}
	// the old cookie is worthless now, even if someone kept a copy
	b.cookie = oldCookie
	wantStatus(t, b.do(http.MethodGet, "/auth/me", ""), http.StatusUnauthorized)
	wantStatus(t, b.do(http.MethodPost, "/auth/logout", ""), http.StatusUnauthorized)
}

func TestSigningOutFailsCleanlyWhenTheStoreDoes(t *testing.T) {
	w := newWorld(t)
	b := w.customer(t, "ada@example.com")
	w.db.DeleteSessionErr = errors.New("connection refused")
	rec := b.do(http.MethodPost, "/auth/logout", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

func TestChangingThePassword(t *testing.T) {
	w := newWorld(t)
	here := w.customer(t, "ada@example.com")
	elsewhere := w.browser(t)
	wantStatus(t, elsewhere.signIn("ada@example.com", password), http.StatusOK)

	rec := here.do(http.MethodPost, "/auth/password", `{"currentPassword":"nope nope nope","newPassword":"a brand new passphrase"}`)
	wantStatus(t, rec, http.StatusForbidden)
	rec = here.do(http.MethodPost, "/auth/password", `{"currentPassword":"`+password+`","newPassword":"short"}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	if !strings.Contains(rec.Body.String(), "newPassword") {
		t.Errorf("the new password was not named: %s", rec.Body)
	}
	wantStatus(t, here.do(http.MethodPost, "/auth/password", `{"currentPassword":"`+password+`","newPassword":"a brand new passphrase"}`), http.StatusNoContent)

	wantStatus(t, here.do(http.MethodGet, "/auth/me", ""), http.StatusOK)
	wantStatus(t, elsewhere.do(http.MethodGet, "/auth/me", ""), http.StatusUnauthorized)
	wantStatus(t, w.browser(t).signIn("ada@example.com", password), http.StatusUnauthorized)
	wantStatus(t, w.browser(t).signIn("ada@example.com", "a brand new passphrase"), http.StatusOK)
	wantStatus(t, here.do(http.MethodPost, "/auth/password", `{"currentPassword":"a brand new passphrase"}`), http.StatusUnprocessableEntity)
}

func TestFailuresOnOurSideAreA500ThatLeaksNothing(t *testing.T) {
	w := newWorld(t)
	w.customer(t, "ada@example.com")
	w.db.GetUserErr = errors.New("connection refused")
	b := w.browser(t)
	rec := b.signIn("ada@example.com", password)
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
	w.db.GetUserErr = nil

	// the session check failing is our fault, not "you are signed out"
	in := w.browser(t)
	wantStatus(t, in.signIn("ada@example.com", password), http.StatusOK)
	w.db.GetSessionErr = errors.New("connection refused")
	rec = in.do(http.MethodGet, "/orders", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

func TestARequestThatChangesSomethingNeedsTheSessionsCSRFToken(t *testing.T) {
	w := newWorld(t)
	b := w.customer(t, "ada@example.com")
	w.db.seedWith("o1", domain.OrderCreated, func(o *domain.Order) { o.CustomerID = b.userID })

	b.noCSRF = true
	rec := b.do(http.MethodPost, "/orders/o1/cancel", "")
	wantStatus(t, rec, http.StatusForbidden)
	wantErrorContains(t, rec, "CSRF")
	b.noCSRF = false

	good := b.csrf
	b.csrf = "x" + good[1:] // same length, wrong content
	wantStatus(t, b.do(http.MethodPost, "/orders/o1/cancel", ""), http.StatusForbidden)
	b.csrf = "short"
	wantStatus(t, b.do(http.MethodPost, "/orders/o1/cancel", ""), http.StatusForbidden)
	if w.db.orders["o1"].State == domain.OrderCancelled {
		t.Fatal("an order was cancelled without the CSRF token")
	}

	b.csrf = good
	wantStatus(t, b.do(http.MethodPost, "/orders/o1/cancel", ""), http.StatusOK)

	// reading needs no token: a page on another site cannot read the answer anyway
	b.noCSRF = true
	wantStatus(t, b.do(http.MethodGet, "/orders/o1", ""), http.StatusOK)
}

func TestAnotherSessionsCSRFTokenDoesNotWork(t *testing.T) {
	w := newWorld(t)
	a := w.customer(t, "ada@example.com")
	c := w.customer(t, "cy@example.com")
	w.db.seedWith("o1", domain.OrderCreated, func(o *domain.Order) { o.CustomerID = c.userID })
	c.csrf = a.csrf // c's own cookie, but a's token
	wantStatus(t, c.do(http.MethodPost, "/orders/o1/cancel", ""), http.StatusForbidden)
	if w.db.orders["o1"].State != domain.OrderCreated {
		t.Error("the order was cancelled with another session's token")
	}
}

// ---------------------------------------------------------------------------
// who may call what
// ---------------------------------------------------------------------------

func TestWithoutAnAccountServiceNobodyCanSignInAndEverythingProtectedIs401(t *testing.T) {
	db := newFakeDB()
	s := NewServer(db, testPayRouter()) // Accounts never set
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/orders"}, {http.MethodPost, "/orders"}, {http.MethodGet, "/auth/me"},
		{http.MethodGet, "/alerts"}, {http.MethodPost, "/orders/o1/pay"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: strings.Repeat("a", 43)})
		if got := serve(s, req).Code; got != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401: with no way to sign in, no request may be let through", c.method, c.path, got)
		}
	}
}

func TestWhatNeedsNoAccountStaysOpen(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct {
		method, path string
		not          int
	}{
		{http.MethodGet, "/health", http.StatusUnauthorized},
		{http.MethodGet, "/dashboard/", http.StatusUnauthorized},
		{http.MethodGet, "/dashboard/app.js", http.StatusUnauthorized},
		{http.MethodGet, "/", http.StatusUnauthorized},
		{http.MethodPost, "/webhooks/stripe", http.StatusUnauthorized}, // proves itself by its signature
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if got := serve(w.s, req).Code; got == c.not {
			t.Errorf("%s %s needs no account but answered %d", c.method, c.path, got)
		}
	}
}

func TestOnlyStaffRunTheKitchenTheDeliveryAndTheAlerts(t *testing.T) {
	w := newWorld(t)
	customer := w.customer(t, "ada@example.com")
	staff := w.staff(t, "dispatch@example.com")
	anonymous := w.browser(t)
	w.db.seedWith("o1", domain.OrderPaid, func(o *domain.Order) { o.CustomerID = customer.userID })

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/orders/o1/accept"}, {http.MethodPost, "/orders/o1/ready"},
		{http.MethodPost, "/orders/o1/pickup"}, {http.MethodPost, "/orders/o1/deliver"},
		{http.MethodGet, "/orders/o1/payment-history"},
		{http.MethodGet, "/alerts"}, {http.MethodPost, "/alerts/1/resolve"},
	} {
		wantStatus(t, anonymous.do(c.method, c.path, ""), http.StatusUnauthorized)
		rec := customer.do(c.method, c.path, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("customer %s %s = %d, want 403 even for their own order", c.method, c.path, rec.Code)
		}
		if code := staff.do(c.method, c.path, "").Code; code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Errorf("staff %s %s = %d, want it let through", c.method, c.path, code)
		}
	}
	if w.db.orders["o1"].State != domain.OrderPaid && w.db.orders["o1"].AssignedDriverID == "" {
		t.Error("state changed by a customer")
	}
}

func TestACustomerSeesAndActsOnOnlyTheirOwnOrders(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	bea := w.customer(t, "bea@example.com")
	w.db.seedWith("ann-1", domain.OrderCreated, func(o *domain.Order) { o.CustomerID = ann.userID })
	w.db.seedWith("bea-1", domain.OrderCreated, func(o *domain.Order) { o.CustomerID = bea.userID })

	// reading
	wantStatus(t, ann.do(http.MethodGet, "/orders/ann-1", ""), http.StatusOK)
	mine := ann.do(http.MethodGet, "/orders/bea-1", "")
	missing := ann.do(http.MethodGet, "/orders/no-such-order", "")
	wantStatus(t, mine, http.StatusNotFound)
	if strings.ReplaceAll(mine.Body.String(), "bea-1", "X") != strings.ReplaceAll(missing.Body.String(), "no-such-order", "X") {
		t.Errorf("someone else's order answers %q but a missing one %q: the difference tells ids exist", mine.Body, missing.Body)
	}
	// acting
	wantStatus(t, ann.do(http.MethodPost, "/orders/bea-1/cancel", ""), http.StatusNotFound)
	wantStatus(t, ann.do(http.MethodPost, "/orders/bea-1/pay", ""), http.StatusNotFound)
	if w.db.orders["bea-1"].State != domain.OrderCreated {
		t.Error("a customer changed someone else's order")
	}
	wantStatus(t, ann.do(http.MethodPost, "/orders/ann-1/cancel", ""), http.StatusOK)

	// listing
	rec := ann.do(http.MethodGet, "/orders", "")
	var list struct {
		Orders []struct {
			ID         string `json:"id"`
			CustomerID string `json:"customerId"`
		} `json:"orders"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Orders) != 1 || list.Orders[0].ID != "ann-1" {
		t.Errorf("ann's list = %d %s, want only ann-1", rec.Code, rec.Body)
	}
	if len(w.db.listedFor) != 1 || w.db.listedFor[0] != ann.userID {
		t.Errorf("listed for %v, want ann only", w.db.listedFor)
	}
}

func TestStaffSeeEveryonesOrders(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	bea := w.customer(t, "bea@example.com")
	staff := w.staff(t, "dispatch@example.com")
	w.db.seedWith("ann-1", domain.OrderCreated, func(o *domain.Order) { o.CustomerID = ann.userID })
	w.db.seedWith("bea-1", domain.OrderCreated, func(o *domain.Order) { o.CustomerID = bea.userID })

	var list struct {
		Orders []struct{ ID string } `json:"orders"`
	}
	rec := staff.do(http.MethodGet, "/orders", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Orders) != 2 {
		t.Errorf("staff list = %s, want both orders", rec.Body)
	}
	wantStatus(t, staff.do(http.MethodGet, "/orders/ann-1", ""), http.StatusOK)
	wantStatus(t, staff.do(http.MethodPost, "/orders/bea-1/cancel", ""), http.StatusOK)
}

func TestACustomerOrdersForThemselvesAndStaffForAnyone(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	staff := w.staff(t, "dispatch@example.com")
	w.db.Open("r1", "Corner Kitchen")
	w.db.Add("meal", "r1", "Meal", 500)
	// a customer orders from the menu; staff may too (their custom lines have their own tests)
	item := `"restaurantId":"r1","paymentType":"card","items":[{"menuItemId":"meal","quantity":1}]`

	// no customer id in the body at all: it is theirs
	rec := ann.do(http.MethodPost, "/orders", `{"id":"o-a",`+item+`}`)
	wantStatus(t, rec, http.StatusCreated)
	if got := w.db.orders["o-a"].CustomerID; got != ann.userID {
		t.Errorf("customer id = %q, want ann's", got)
	}
	// their own id is fine too
	wantStatus(t, ann.do(http.MethodPost, "/orders", `{"id":"o-b","customerId":"`+ann.userID+`",`+item+`}`), http.StatusCreated)
	// someone else's is refused, not quietly changed
	rec = ann.do(http.MethodPost, "/orders", `{"id":"o-c","customerId":"somebody-else",`+item+`}`)
	wantStatus(t, rec, http.StatusForbidden)
	if _, made := w.db.orders["o-c"]; made {
		t.Error("an order was placed in someone else's name")
	}
	// staff place orders for anyone
	wantStatus(t, staff.do(http.MethodPost, "/orders", `{"id":"o-d","customerId":"walk-in-7",`+item+`}`), http.StatusCreated)
	if got := w.db.orders["o-d"].CustomerID; got != "walk-in-7" {
		t.Errorf("customer id = %q", got)
	}
}

func TestAFailureListingACustomersOrdersIsAnInternalError(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	w.db.listErr = errors.New("connection refused")
	rec := ann.do(http.MethodGet, "/orders", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
}

// The guard fails closed for anything that is neither role.
func TestAnUnknownRoleIsTreatedAsNoAccess(t *testing.T) {
	order := domain.Order{ID: "o1", CustomerID: "u1"}
	for name, p := range map[string]auth.Principal{
		"nobody":                 {},
		"a customer with no id":  {User: domain.User{Role: domain.RoleCustomer}},
		"a role that is not one": {User: domain.User{ID: "u1", Role: "admin"}},
		"another customer":       {User: domain.User{ID: "u2", Role: domain.RoleCustomer}},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(withPrincipal(req, p))
		if mayAccess(req, order) {
			t.Errorf("%s may access someone's order", name)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withPrincipal(req, auth.Principal{User: domain.User{ID: "u1", Role: domain.RoleCustomer}}))
	if !mayAccess(req, order) {
		t.Error("a customer cannot access their own order")
	}

	// and an odd role never gets staff-only routes
	db := newFakeDB()
	s := NewServer(db, testPayRouter())
	s.Identify = func(*http.Request) (auth.Principal, error) {
		return auth.Principal{User: domain.User{ID: "u1", Role: "admin"}}, nil
	}
	wantStatus(t, serve(s, httptest.NewRequest(http.MethodGet, "/alerts", nil)), http.StatusForbidden)
}

func TestTheClientAddressIsWhereTheConnectionCameFrom(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9:5555": "203.0.113.9", "[2001:db8::1]:443": "2001:db8::1", "no-port": "no-port",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = in
		if got := clientIP(r); got != want {
			t.Errorf("clientIP(%q) = %q, want %q", in, got, want)
		}
	}
	// a forwarding header is not believed: it could be made up by the client
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("clientIP = %q: a header the client can set must not choose the address that is limited", got)
	}
}

func TestCORSGivesCredentialsOnlyToAnOriginWeNamed(t *testing.T) {
	w := newWorld(t)
	for origin, want := range map[string]string{"http://localhost:3000": "true", "https://evil.example": ""} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		w.s.Handler().ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != want {
			t.Errorf("origin %s: Allow-Credentials = %q, want %q", origin, got, want)
		}
	}
}

// The account is made and then signing it in fails: the person is told to try again,
// and the account is there, so signing in later works.
func TestASignUpWhoseAutomaticSignInFailsLeavesTheAccountAndAnError(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t)
	w.db.CreateSessionErr = errors.New("connection refused")
	rec := b.do(http.MethodPost, "/auth/signup", `{"email":"ada@example.com","name":"Ada","password":"`+password+`"}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	if cookieNamed(rec, sessionCookie) != nil || strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("answer = %s, cookie = %v: no cookie and no cause", rec.Body, cookieNamed(rec, sessionCookie))
	}
	w.db.CreateSessionErr = nil
	wantStatus(t, b.signIn("ada@example.com", password), http.StatusOK)
}

func TestMalformedBodiesOnSigningInAndChangingThePasswordAre400(t *testing.T) {
	w := newWorld(t)
	b := w.customer(t, "ada@example.com")
	wantStatus(t, b.do(http.MethodPost, "/auth/login", `{"email":`), http.StatusBadRequest)
	wantStatus(t, b.do(http.MethodPost, "/auth/login", `{"email":"a@b.co","password":"x","extra":1}`), http.StatusBadRequest)
	wantStatus(t, b.do(http.MethodPost, "/auth/password", `{"currentPassword":`), http.StatusBadRequest)
}

func TestLimitsCountEachAddressSeparately(t *testing.T) {
	w := newWorld(t)
	a, b := w.browser(t), w.browser(t)
	b.ip = "198.51.100.8:4000"

	for i := range 10 {
		wantStatus(t, a.do(http.MethodPost, "/auth/signup", `{"email":"a`+strconv.Itoa(i)+`@example.com","name":"A","password":"`+password+`"}`), http.StatusCreated)
	}
	wantStatus(t, a.do(http.MethodPost, "/auth/signup", `{"email":"a-more@example.com","name":"A","password":"`+password+`"}`), http.StatusTooManyRequests)
	b.signUp("ada@example.com") // another address is not held up by the first

	for i := range 30 {
		wantStatus(t, a.signIn("guess"+strconv.Itoa(i)+"@example.com", "wrong password here"), http.StatusUnauthorized)
	}
	wantStatus(t, a.signIn("ada@example.com", password), http.StatusTooManyRequests)
	wantStatus(t, b.signIn("ada@example.com", password), http.StatusOK)
}

func TestAWaitIsRoundedUpToWholeSeconds(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAuthError(rec, &auth.TooManyAttemptsError{RetryAfter: 1500 * time.Millisecond})
	wantStatus(t, rec, http.StatusTooManyRequests)
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2: 1.5 seconds must not be said as 1", got)
	}
}

func TestTheClearedCookieIsSecureWhereTheSessionCookieWas(t *testing.T) {
	w := newWorld(t)
	w.s.SecureCookies = true
	b := w.customer(t, "ada@example.com")
	rec := b.do(http.MethodPost, "/auth/logout", "")
	if c := cookieNamed(rec, sessionCookie); c == nil || !c.Secure || c.MaxAge >= 0 {
		t.Errorf("cleared cookie = %+v, want Secure and expired", c)
	}

	// and over TLS without the setting
	w2 := newWorld(t)
	b2 := w2.customer(t, "ada@example.com")
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.TLS = &tls.ConnectionState{}
	req.AddCookie(b2.cookie)
	req.Header.Set(csrfHeader, b2.csrf)
	if c := cookieNamed(serve(w2.s, req), sessionCookie); c == nil || !c.Secure {
		t.Errorf("over TLS the cleared cookie = %+v, want Secure", c)
	}
}
