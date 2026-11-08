package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swiftdrop/internal/auth"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Managing accounts over HTTP. These servers use the real auth service on the in-memory store
// and NO Identify override, so a request is who its cookie says.
// ---------------------------------------------------------------------------

type accountBody struct {
	User              domain.User        `json:"user"`
	TemporaryPassword string             `json:"temporaryPassword"`
	Accounts          []domain.User      `json:"accounts"`
	Events            []domain.UserEvent `json:"events"`
	Error             string             `json:"error"`
	Code              string             `json:"code"`
	Fields            map[string]string  `json:"fields"`
}

func decodeAccounts(t *testing.T, rec *httptest.ResponseRecorder) accountBody {
	t.Helper()
	var b accountBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil && rec.Body.Len() > 0 {
		t.Fatalf("not JSON: %v (%s)", err, rec.Body)
	}
	return b
}

// makeAccount has the staff browser make an account and returns it with its temporary password.
func makeAccount(t *testing.T, staff *browser, email, role string) (domain.User, string) {
	t.Helper()
	rec := staff.do(http.MethodPost, "/users", `{"email":"`+email+`","name":"Pat Person","role":"`+role+`"}`)
	wantStatus(t, rec, http.StatusCreated)
	b := decodeAccounts(t, rec)
	return b.User, b.TemporaryPassword
}

// ---- who may ------------------------------------------------------------------------

func TestOnlyStaffManageAccounts(t *testing.T) {
	w := newWorld(t)
	customer := w.customer(t, "ada@example.com")
	staff := w.staff(t, "dee@example.com")
	anonymous := w.browser(t)
	target, _ := makeAccount(t, staff, "pat@example.com", "customer")

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/users", ""}, {http.MethodGet, "/users/" + target.ID, ""},
		{http.MethodPost, "/users", `{"email":"x@example.com","name":"X","role":"customer"}`},
		{http.MethodPost, "/users/" + target.ID + "/disable", ""}, {http.MethodPost, "/users/" + target.ID + "/enable", ""},
		{http.MethodPost, "/users/" + target.ID + "/reset-password", ""},
	} {
		wantStatus(t, anonymous.do(c.method, c.path, c.body), http.StatusUnauthorized)
		if got := customer.do(c.method, c.path, c.body).Code; got != http.StatusForbidden {
			t.Errorf("customer %s %s = %d, want 403", c.method, c.path, got)
		}
		if got := staff.do(c.method, c.path, c.body).Code; got == http.StatusUnauthorized || got == http.StatusForbidden {
			t.Errorf("staff %s %s = %d, want it let through", c.method, c.path, got)
		}
	}
	if _, ok := w.db.Users["x@example.com"]; ok && w.db.Users["x@example.com"].User.Role == "" {
		t.Error("an account was made by someone who may not")
	}
}

func TestEveryChangeNeedsTheSessionsCSRFToken(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	target, _ := makeAccount(t, staff, "pat@example.com", "customer")
	staff.noCSRF = true
	for _, c := range []struct{ path, body string }{
		{"/users", `{"email":"x@example.com","name":"X","role":"customer"}`},
		{"/users/" + target.ID + "/disable", ""}, {"/users/" + target.ID + "/enable", ""},
		{"/users/" + target.ID + "/reset-password", ""},
	} {
		wantStatus(t, staff.do(http.MethodPost, c.path, c.body), http.StatusForbidden)
	}
	if u := w.db.Users["pat@example.com"].User; u.Disabled() || !u.MustChangePassword {
		t.Errorf("an account was changed without the token: %+v", u)
	}
}

func TestTheRoutesTakeOnlyTheirOwnMethods(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/users"}, {http.MethodDelete, "/users"}, {http.MethodPost, "/users/x"}, {http.MethodGet, "/users/x/disable"},
		{http.MethodGet, "/users/x/reset-password"}, {http.MethodDelete, "/users/x"}, {http.MethodGet, "/users/x/enable"},
	} {
		if got := staff.do(c.method, c.path, "").Code; got != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, got)
		}
	}
}

// ---- listing -----------------------------------------------------------------------

func TestTheListHasNoSecretsAndIsNeverCached(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	w.customer(t, "ada@example.com")
	rec := staff.do(http.MethodGet, "/users", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	body := strings.ToLower(rec.Body.String())
	for _, leak := range []string{"hash", "argon2", "password\":", goodPasswordForLeakCheck} {
		if strings.Contains(body, leak) {
			t.Errorf("the list contains %q: %s", leak, rec.Body)
		}
	}
	if got := decodeAccounts(t, rec).Accounts; len(got) != 2 {
		t.Errorf("%d accounts, want 2", len(got))
	}
}

const goodPasswordForLeakCheck = "a long enough phrase"

func TestTheListCanBeFilteredAndSearched(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	ada, _ := makeAccount(t, staff, "ada@example.com", "customer")
	makeAccount(t, staff, "cy@example.com", "customer")
	makeAccount(t, staff, "second@example.com", "staff")
	wantStatus(t, staff.do(http.MethodPost, "/users/"+ada.ID+"/disable", ""), http.StatusOK)

	emails := func(query string) string {
		rec := staff.do(http.MethodGet, "/users"+query, "")
		wantStatus(t, rec, http.StatusOK)
		var out []string
		for _, u := range decodeAccounts(t, rec).Accounts {
			out = append(out, u.Email)
		}
		return strings.Join(out, ",")
	}
	if got := emails("?role=staff"); !strings.Contains(got, "dee@") || !strings.Contains(got, "second@") || strings.Contains(got, "cy@") {
		t.Errorf("staff = %q", got)
	}
	if got := emails("?status=disabled"); got != "ada@example.com" {
		t.Errorf("disabled = %q", got)
	}
	if got := emails("?role=customer&status=active"); got != "cy@example.com" {
		t.Errorf("active customers = %q", got)
	}
	if got := emails("?q=CY@"); got != "cy@example.com" {
		t.Errorf("search = %q", got)
	}
	if got := emails("?limit=1"); strings.Contains(got, ",") || got == "" {
		t.Errorf("limit 1 = %q", got)
	}
	if got := emails("?q=nobody-by-this-name"); got != "" {
		t.Errorf("no match = %q", got)
	}
	// an empty list is a list, not null
	rec := staff.do(http.MethodGet, "/users?q=nobody-by-this-name", "")
	if !strings.Contains(rec.Body.String(), `"accounts":[]`) {
		t.Errorf("empty answer = %s", rec.Body)
	}
}

func TestAListThatAsksForNonsenseIsRefused(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	for _, q := range []string{"?role=admin", "?status=gone", "?limit=0", "?limit=-3", "?limit=lots", "?limit=1.5"} {
		wantStatus(t, staff.do(http.MethodGet, "/users"+q, ""), http.StatusBadRequest)
	}
}

// ---- making one --------------------------------------------------------------------

func TestAnAccountIsMadeWithATemporaryPasswordThatIsShownOnceAndNeverKept(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	logs := captureLog(t)

	rec := staff.do(http.MethodPost, "/users", `{"email":"  New@Example.com ","name":" New Person ","role":"staff"}`)
	wantStatus(t, rec, http.StatusCreated)
	b := decodeAccounts(t, rec)
	if b.User.Email != "new@example.com" || b.User.Role != domain.RoleStaff || !b.User.MustChangePassword || len(b.TemporaryPassword) != 24 {
		t.Errorf("answer = %+v", b)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q: a password must not be cached", rec.Header().Get("Cache-Control"))
	}
	if strings.Contains(logs.String(), b.TemporaryPassword) {
		t.Errorf("the temporary password was logged:\n%s", logs)
	}
	if !strings.Contains(logs.String(), "new@example.com") || !strings.Contains(logs.String(), "dee@example.com") {
		t.Errorf("who made whom is not in the log:\n%s", logs)
	}
	// the list never shows it again
	if strings.Contains(staff.do(http.MethodGet, "/users", "").Body.String(), b.TemporaryPassword) {
		t.Error("the list repeats the temporary password")
	}
}

func TestMakingAnAccountNamesWhatIsWrongOrTaken(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")

	rec := staff.do(http.MethodPost, "/users", `{"email":"nope","name":"","role":"admin"}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	f := decodeAccounts(t, rec).Fields
	if f["email"] == "" || f["name"] == "" || f["role"] == "" {
		t.Errorf("fields = %v, want all three", f)
	}
	rec = staff.do(http.MethodPost, "/users", `{"email":"dee@example.com","name":"Dee","role":"staff"}`)
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "already exists")

	wantStatus(t, staff.do(http.MethodPost, "/users", `{"email":`), http.StatusBadRequest)
	wantStatus(t, staff.do(http.MethodPost, "/users", `{"email":"a@b.co","name":"A","role":"staff","password":"mine"}`), http.StatusBadRequest) // nobody picks it for them
}

// ---- the temporary password must be replaced --------------------------------------

func TestAnAccountWithATemporaryPasswordCanDoNothingButReplaceIt(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	_, temp := makeAccount(t, staff, "pat@example.com", "staff")

	pat := w.browser(t)
	rec := pat.signIn("pat@example.com", temp)
	wantStatus(t, rec, http.StatusOK)
	if !decodeAccounts(t, rec).User.MustChangePassword {
		t.Fatal("the sign-in does not say the password must be changed")
	}

	// everything but the three routes is refused, with a code the page can act on
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/orders", ""}, {http.MethodGet, "/orders/o1", ""}, {http.MethodPost, "/orders", validBody("o-1")},
		{http.MethodGet, "/alerts", ""}, {http.MethodGet, "/users", ""},
		{http.MethodPost, "/users", `{"email":"x@example.com","name":"X","role":"staff"}`}, {http.MethodPost, "/orders/o1/accept", ""},
	} {
		rec := pat.do(c.method, c.path, c.body)
		wantStatus(t, rec, http.StatusForbidden)
		if code := decodeAccounts(t, rec).Code; code != "password_change_required" {
			t.Errorf("%s %s: code = %q, want password_change_required", c.method, c.path, code)
		}
	}
	if _, ok := w.db.Users["x@example.com"]; ok {
		t.Error("an account was made by someone who has not replaced their temporary password")
	}
	wantStatus(t, pat.do(http.MethodGet, "/auth/me", ""), http.StatusOK)

	// choosing their own opens everything up
	wantStatus(t, pat.do(http.MethodPost, "/auth/password", `{"currentPassword":"`+temp+`","newPassword":"my own long passphrase"}`), http.StatusNoContent)
	wantStatus(t, pat.do(http.MethodGet, "/orders", ""), http.StatusOK)
	if decodeAccounts(t, pat.do(http.MethodGet, "/auth/me", "")).User.MustChangePassword {
		t.Error("the account still says its password must be changed")
	}
}

func TestSigningOutIsAlwaysPossible(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	_, temp := makeAccount(t, staff, "pat@example.com", "customer")
	pat := w.browser(t)
	wantStatus(t, pat.signIn("pat@example.com", temp), http.StatusOK)
	wantStatus(t, pat.do(http.MethodPost, "/auth/logout", ""), http.StatusNoContent)
}

// ---- one account ------------------------------------------------------------------

func TestOneAccountComesWithItsHistory(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	pat, _ := makeAccount(t, staff, "pat@example.com", "customer")
	wantStatus(t, staff.do(http.MethodPost, "/users/"+pat.ID+"/disable", ""), http.StatusOK)

	rec := staff.do(http.MethodGet, "/users/"+pat.ID, "")
	wantStatus(t, rec, http.StatusOK)
	b := decodeAccounts(t, rec)
	if b.User.Email != "pat@example.com" || !b.User.Disabled() || len(b.Events) != 2 || b.Events[0].Kind != "disabled" || b.Events[1].Kind != "created" {
		t.Errorf("answer = %+v", b)
	}
	if b.Events[0].ActorEmail != "dee@example.com" {
		t.Errorf("event = %+v, want it to name who did it", b.Events[0])
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the history may be cached")
	}
	wantStatus(t, staff.do(http.MethodGet, "/users/ghost", ""), http.StatusNotFound)
}

// ---- switching off and on ---------------------------------------------------------

func TestDisablingSignsThemOutAtOnceAndEnablingLetsThemBack(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	ada := w.customer(t, "ada@example.com")
	wantStatus(t, ada.do(http.MethodGet, "/orders", ""), http.StatusOK)

	rec := staff.do(http.MethodPost, "/users/"+ada.userID+"/disable", "")
	wantStatus(t, rec, http.StatusOK)
	if !decodeAccounts(t, rec).User.Disabled() {
		t.Error("the answer does not show the account disabled")
	}
	wantStatus(t, ada.do(http.MethodGet, "/orders", ""), http.StatusUnauthorized) // her very next request
	wantStatus(t, w.browser(t).signIn("ada@example.com", password), http.StatusUnauthorized)

	wantStatus(t, staff.do(http.MethodPost, "/users/"+ada.userID+"/enable", ""), http.StatusOK)
	wantStatus(t, w.browser(t).signIn("ada@example.com", password), http.StatusOK)
}

func TestDisablingTwiceIsFine(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	ada := w.customer(t, "ada@example.com")
	wantStatus(t, staff.do(http.MethodPost, "/users/"+ada.userID+"/disable", ""), http.StatusOK)
	wantStatus(t, staff.do(http.MethodPost, "/users/"+ada.userID+"/disable", ""), http.StatusOK)
}

func TestNobodyDisablesTheirOwnAccount(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	second := w.staff(t, "second@example.com")

	rec := staff.do(http.MethodPost, "/users/"+staff.userID+"/disable", "")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "your own account")
	wantStatus(t, staff.do(http.MethodGet, "/orders", ""), http.StatusOK) // still signed in

	// another staff member may switch them off, and then cannot be switched off themselves by
	// the one they disabled: that account is signed out at once
	wantStatus(t, second.do(http.MethodPost, "/users/"+staff.userID+"/disable", ""), http.StatusOK)
	wantStatus(t, staff.do(http.MethodPost, "/users/"+second.userID+"/disable", ""), http.StatusUnauthorized)
}

func TestAnAccountThatIsNotThereIsA404(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	for _, path := range []string{"/users/ghost/disable", "/users/ghost/enable", "/users/ghost/reset-password"} {
		wantStatus(t, staff.do(http.MethodPost, path, ""), http.StatusNotFound)
	}
}

// ---- resetting a password ---------------------------------------------------------

func TestResettingGivesATemporaryPasswordShownOnceAndSignsThemOut(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	ada := w.customer(t, "ada@example.com")
	logs := captureLog(t)

	rec := staff.do(http.MethodPost, "/users/"+ada.userID+"/reset-password", "")
	wantStatus(t, rec, http.StatusOK)
	b := decodeAccounts(t, rec)
	if len(b.TemporaryPassword) != 24 || !b.User.MustChangePassword {
		t.Errorf("answer = %+v", b)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a password may be cached")
	}
	if strings.Contains(logs.String(), b.TemporaryPassword) {
		t.Errorf("the temporary password was logged:\n%s", logs)
	}
	wantStatus(t, ada.do(http.MethodGet, "/orders", ""), http.StatusUnauthorized)
	wantStatus(t, w.browser(t).signIn("ada@example.com", password), http.StatusUnauthorized)
	wantStatus(t, w.browser(t).signIn("ada@example.com", b.TemporaryPassword), http.StatusOK)
}

func TestNobodyResetsTheirOwnPasswordThatWay(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	rec := staff.do(http.MethodPost, "/users/"+staff.userID+"/reset-password", "")
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "your own account")
	wantStatus(t, staff.signIn("dee@example.com", password), http.StatusOK) // their password is as it was
}

// ---- when storage fails -----------------------------------------------------------

func TestAnAccountStorageFailureIsA500ThatLeaksNothing(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dee@example.com")
	pat, _ := makeAccount(t, staff, "pat@example.com", "customer")
	w.db.AdminErr = errors.New(`pq: connection to "10.1.2.3" refused`)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/users", ""}, {http.MethodGet, "/users/" + pat.ID, ""},
		{http.MethodPost, "/users", `{"email":"x@example.com","name":"X","role":"staff"}`},
		{http.MethodPost, "/users/" + pat.ID + "/disable", ""}, {http.MethodPost, "/users/" + pat.ID + "/reset-password", ""},
	} {
		rec := staff.do(c.method, c.path, c.body)
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "10.1.2.3") {
			t.Errorf("%s %s leaks the cause: %s", c.method, c.path, rec.Body)
		}
	}
}

func TestTheServicesSecondLockIsAForbiddenToo(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAuthError(rec, auth.ErrNotStaff)
	wantStatus(t, rec, http.StatusForbidden)
	rec = httptest.NewRecorder()
	writeAuthError(rec, ports.ErrLastStaff)
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "no active staff")
}
