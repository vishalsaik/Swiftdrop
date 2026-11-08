package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// managing other people's accounts: the rules, with the in-memory store
// ---------------------------------------------------------------------------

// staffIn makes a staff account and signs it in.
func (r *rig) staffIn(t *testing.T, email string) Principal {
	t.Helper()
	if _, err := r.svc.CreateStaff(ctx, email, "Dee Patch", goodPassword); err != nil {
		t.Fatal(err)
	}
	_, p := r.signedIn(t, email)
	return p
}

// customerIn makes a customer and signs them in.
func (r *rig) customerIn(t *testing.T, email string) Principal {
	t.Helper()
	r.signedUp(t, email)
	_, p := r.signedIn(t, email)
	return p
}

func (r *rig) eventsOf(t *testing.T, id string) []domain.UserEvent {
	t.Helper()
	ev, err := r.store.ListUserEvents(ctx, id, 100)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func kinds(ev []domain.UserEvent) string {
	var out []string
	for i := len(ev) - 1; i >= 0; i-- { // oldest first reads like a story
		out = append(out, ev[i].Kind)
	}
	return strings.Join(out, ",")
}

// ---- who may ----------------------------------------------------------------------

func TestOnlyStaffManageAccounts(t *testing.T) {
	r := newRig()
	customer := r.customerIn(t, "ada@example.com")
	for name, who := range map[string]Principal{"a customer": customer, "nobody": {}} {
		if _, err := r.svc.ListAccounts(ctx, who, ports.UserQuery{}); !errors.Is(err, ErrNotStaff) {
			t.Errorf("%s ListAccounts: %v", name, err)
		}
		if _, _, err := r.svc.AccountDetail(ctx, who, "x"); !errors.Is(err, ErrNotStaff) {
			t.Errorf("%s AccountDetail: %v", name, err)
		}
		if _, _, err := r.svc.CreateAccount(ctx, who, "new@example.com", "New", domain.RoleStaff); !errors.Is(err, ErrNotStaff) {
			t.Errorf("%s CreateAccount: %v", name, err)
		}
		if _, err := r.svc.SetDisabled(ctx, who, "x", true); !errors.Is(err, ErrNotStaff) {
			t.Errorf("%s SetDisabled: %v", name, err)
		}
		if _, _, err := r.svc.ResetPassword(ctx, who, "x"); !errors.Is(err, ErrNotStaff) {
			t.Errorf("%s ResetPassword: %v", name, err)
		}
	}
	if len(r.store.Users) != 1 {
		t.Errorf("%d accounts, want only the customer: nothing may be made by someone who may not", len(r.store.Users))
	}
}

// ---- making one -------------------------------------------------------------------

func TestAnAccountMadeByStaffHasATemporaryPasswordThatMustBeReplaced(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")

	user, password, err := r.svc.CreateAccount(ctx, staff, "  New.Person@Example.COM ", "  New Person ", domain.RoleStaff)
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "new.person@example.com" || user.Name != "New Person" || user.Role != domain.RoleStaff || !user.MustChangePassword || user.ID == "" {
		t.Errorf("user = %+v", user)
	}
	if len(password) != 24 || !regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`).MatchString(password) {
		t.Errorf("temporary password %q is not 24 URL-safe characters", password)
	}
	// only a slow hash of it is kept
	stored := r.store.Users["new.person@example.com"]
	if stored.Hash == password || strings.Contains(stored.Hash, password) {
		t.Error("the temporary password is stored in the clear")
	}
	if ok, err := Verify(password, stored.Hash); err != nil || !ok {
		t.Errorf("the temporary password does not match its hash: %v %v", ok, err)
	}
	// it signs in, and the sign-in says it must be replaced
	_, p, err := r.svc.SignIn(ctx, "1.1.1.1", "new.person@example.com", password)
	if err != nil || !p.User.MustChangePassword {
		t.Errorf("sign-in = %+v, %v, want it to say the password must be changed", p, err)
	}
	if got := kinds(r.eventsOf(t, user.ID)); got != "created" {
		t.Errorf("history = %q", got)
	}
	if ev := r.eventsOf(t, user.ID)[0]; ev.ActorID != staff.User.ID || ev.ActorEmail != "dee@example.com" || ev.Detail != "role=staff" {
		t.Errorf("event = %+v, want it to name who made it and as what", ev)
	}
}

func TestEveryTemporaryPasswordIsDifferent(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	seen := map[string]bool{}
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		_, pw, err := r.svc.CreateAccount(ctx, staff, email, "Name", domain.RoleCustomer)
		if err != nil || seen[pw] {
			t.Fatalf("password %q repeated or error %v", pw, err)
		}
		seen[pw] = true
	}
}

func TestAnAccountMadeByStaffCanBeACustomerToo(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	user, _, err := r.svc.CreateAccount(ctx, staff, "c@example.com", "Cy", domain.RoleCustomer)
	if err != nil || user.Role != domain.RoleCustomer {
		t.Errorf("user = %+v, err = %v", user, err)
	}
}

func TestMakingAnAccountNamesEveryFieldThatIsWrongAndMakesNothing(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	_, _, err := r.svc.CreateAccount(ctx, staff, "nope", "  ", domain.Role("admin"))
	var invalid *InvalidError
	if !errors.As(err, &invalid) || invalid.Fields["email"] == "" || invalid.Fields["name"] == "" || invalid.Fields["role"] == "" {
		t.Fatalf("err = %v, want all three named", err)
	}
	if _, _, err := r.svc.CreateAccount(ctx, staff, "ok@example.com", "Ok", domain.Role("")); !errors.As(err, &invalid) || invalid.Fields["role"] == "" {
		t.Errorf("no role: %v", err)
	}
	if len(r.store.Users) != 1 {
		t.Errorf("%d accounts, want only the staff member's own", len(r.store.Users))
	}
}

func TestAnEmailThatIsTakenIsRefusedHoweverItIsWritten(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	if _, _, err := r.svc.CreateAccount(ctx, staff, "x@example.com", "X", domain.RoleCustomer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.CreateAccount(ctx, staff, "X@EXAMPLE.com", "Other", domain.RoleStaff); !errors.Is(err, ports.ErrEmailTaken) {
		t.Errorf("err = %v, want ErrEmailTaken", err)
	}
}

func TestMakingAnAccountStopsWhenSomethingFails(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")

	r.store.AdminErr = errors.New("connection refused")
	if _, pw, err := r.svc.CreateAccount(ctx, staff, "a@example.com", "A", domain.RoleStaff); err == nil || pw != "" {
		t.Errorf("a failing store: pw=%q err=%v, want the error and no password to hand out", pw, err)
	}
	r.store.AdminErr = nil

	withEntropy(t, 0)
	if _, pw, err := r.svc.CreateAccount(ctx, staff, "b@example.com", "B", domain.RoleStaff); err == nil || pw != "" {
		t.Errorf("no randomness: pw=%q err=%v, want an error and no password", pw, err)
	}
	if _, ok := r.store.Users["b@example.com"]; ok {
		t.Error("an account was made with no randomness for its password")
	}
}

func TestMakingAnAccountGivesUpWhenTheCallerDoesWhileWaitingToHash(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	for range cap(r.svc.slots) {
		r.svc.slots <- struct{}{} // every hashing slot is busy
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.svc.CreateAccount(cctx, staff, "a@example.com", "A", domain.RoleStaff); err == nil {
		t.Error("an account was made although the caller had gone")
	}
	if _, _, err := r.svc.ResetPassword(cctx, staff, "whoever"); err == nil {
		t.Error("a password was reset although the caller had gone")
	}
}

// ---- switching off and on ---------------------------------------------------------

func TestDisablingAnAccountSignsThemOutAtOnceAndEnablingLetsThemBack(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	ada := r.customerIn(t, "ada@example.com")
	tokenA, _ := r.signedIn(t, "ada@example.com")
	tokenB, _ := r.signedIn(t, "ada@example.com") // two devices

	user, err := r.svc.SetDisabled(ctx, staff, ada.User.ID, true)
	if err != nil || user.DisabledAt == nil {
		t.Fatalf("SetDisabled = %+v, %v", user, err)
	}
	for _, tok := range []string{tokenA, tokenB} {
		if _, err := r.svc.Authenticate(ctx, tok); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("a sign-in of the disabled account still works: %v", err)
		}
	}
	if n := r.store.SessionCount(); n != 1 { // only the staff member's own is left
		t.Errorf("%d sessions left, want 1 (the staff member's)", n)
	}
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a disabled account signed in: %v", err)
	}

	user, err = r.svc.SetDisabled(ctx, staff, ada.User.ID, false)
	if err != nil || user.DisabledAt != nil {
		t.Fatalf("enable = %+v, %v", user, err)
	}
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err != nil {
		t.Errorf("an enabled account cannot sign in: %v", err)
	}
	if got := kinds(r.eventsOf(t, ada.User.ID)); got != "disabled,enabled" {
		t.Errorf("history = %q", got)
	}
}

func TestDoingItAgainChangesNothingAndWritesNothing(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	ada := r.customerIn(t, "ada@example.com")
	if _, err := r.svc.SetDisabled(ctx, staff, ada.User.ID, false); err != nil { // already enabled
		t.Fatal(err)
	}
	if _, err := r.svc.SetDisabled(ctx, staff, ada.User.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.SetDisabled(ctx, staff, ada.User.ID, true); err != nil { // already disabled
		t.Fatal(err)
	}
	if got := kinds(r.eventsOf(t, ada.User.ID)); got != "disabled" {
		t.Errorf("history = %q, want a single line", got)
	}
}

func TestNobodyDisablesTheirOwnAccount(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	r.staffIn(t, "second@example.com")
	if _, err := r.svc.SetDisabled(ctx, staff, staff.User.ID, true); !errors.Is(err, ports.ErrOwnAccount) {
		t.Errorf("err = %v, want ErrOwnAccount even with another staff member to spare", err)
	}
}

func TestTheLastActiveStaffAccountCannotBeDisabled(t *testing.T) {
	r := newRig()
	first := r.staffIn(t, "first@example.com")
	second := r.staffIn(t, "second@example.com")

	if _, err := r.svc.SetDisabled(ctx, first, second.User.ID, true); err != nil {
		t.Fatalf("disabling one of two: %v", err)
	}
	// only the first is active now: nobody may switch them off, not even another staff member
	third := r.customerIn(t, "cy@example.com")
	if _, err := r.svc.SetDisabled(ctx, Principal{User: domain.User{ID: "someone-else", Role: domain.RoleStaff, Email: "x"}}, first.User.ID, true); !errors.Is(err, ports.ErrLastStaff) {
		t.Errorf("err = %v, want ErrLastStaff", err)
	}
	// a customer is not staff, so disabling one is no problem
	if _, err := r.svc.SetDisabled(ctx, first, third.User.ID, true); err != nil {
		t.Errorf("disabling a customer: %v", err)
	}
	// and once the other is enabled again, the first may be disabled
	if _, err := r.svc.SetDisabled(ctx, first, second.User.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestAnAccountThatIsNotThereIsNotFound(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	if _, err := r.svc.SetDisabled(ctx, staff, "ghost", true); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("disable: %v", err)
	}
	if _, _, err := r.svc.ResetPassword(ctx, staff, "ghost"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("reset: %v", err)
	}
	if _, _, err := r.svc.AccountDetail(ctx, staff, "ghost"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("detail: %v", err)
	}
}

// ---- resetting a password ---------------------------------------------------------

func TestResettingAPasswordReplacesItSignsThemOutAndMakesThemChooseAgain(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	ada := r.customerIn(t, "ada@example.com")
	token, _ := r.signedIn(t, "ada@example.com")

	user, temp, err := r.svc.ResetPassword(ctx, staff, ada.User.ID)
	if err != nil || !user.MustChangePassword || len(temp) != 24 {
		t.Fatalf("ResetPassword = %+v, %q, %v", user, temp, err)
	}
	if _, err := r.svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("a sign-in made with the old password still works: %v", err)
	}
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("the old password still works: %v", err)
	}
	_, p, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", temp)
	if err != nil || !p.User.MustChangePassword {
		t.Errorf("the temporary password: %+v, %v", p, err)
	}
	if got := kinds(r.eventsOf(t, ada.User.ID)); got != "password_reset" {
		t.Errorf("history = %q", got)
	}
	if ev := r.eventsOf(t, ada.User.ID)[0]; ev.ActorEmail != "dee@example.com" {
		t.Errorf("event = %+v", ev)
	}
}

func TestNobodyResetsTheirOwnPasswordThatWay(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	if _, _, err := r.svc.ResetPassword(ctx, staff, staff.User.ID); !errors.Is(err, ports.ErrOwnAccount) {
		t.Errorf("err = %v, want ErrOwnAccount: they have the change-password form", err)
	}
	if _, _, err := r.svc.ResetPassword(ctx, staff, "x"); errors.Is(err, ports.ErrOwnAccount) {
		t.Error("another account was treated as one's own")
	}
}

func TestAFailureResettingGivesOutNoPassword(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	ada := r.customerIn(t, "ada@example.com")
	r.store.AdminErr = errors.New("connection refused")
	if _, pw, err := r.svc.ResetPassword(ctx, staff, ada.User.ID); err == nil || pw != "" {
		t.Errorf("pw=%q err=%v, want the error and no password", pw, err)
	}
	r.store.AdminErr = nil
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err != nil {
		t.Errorf("the old password stopped working although the reset failed: %v", err)
	}
	withEntropy(t, 0)
	if _, pw, err := r.svc.ResetPassword(ctx, staff, ada.User.ID); err == nil || pw != "" {
		t.Errorf("no randomness: pw=%q err=%v", pw, err)
	}
}

// ---- choosing their own -----------------------------------------------------------

func TestChoosingYourOwnPasswordEndsTheTemporaryOneAndIsRecorded(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	user, temp, err := r.svc.CreateAccount(ctx, staff, "new@example.com", "New", domain.RoleCustomer)
	if err != nil {
		t.Fatal(err)
	}
	token, p, err := r.svc.SignIn(ctx, "1.1.1.1", "new@example.com", temp)
	if err != nil || !p.User.MustChangePassword {
		t.Fatalf("sign-in: %+v %v", p, err)
	}
	if err := r.svc.ChangePassword(ctx, p, temp, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	again, err := r.svc.Authenticate(ctx, token)
	if err != nil || again.User.MustChangePassword {
		t.Errorf("after choosing their own: %+v, %v, want the mark gone", again, err)
	}
	if got := kinds(r.eventsOf(t, user.ID)); got != "created,password_changed" {
		t.Errorf("history = %q", got)
	}
	if ev := r.eventsOf(t, user.ID)[0]; ev.ActorID != user.ID || ev.Kind != domain.UserPasswordChanged {
		t.Errorf("event = %+v, want the account itself as the actor", ev)
	}
}

// ---- reading ---------------------------------------------------------------------

func TestListingAccountsHasADefaultAndAMaximumAndTrimsTheSearch(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	for _, c := range []struct {
		asked, want int
	}{{0, 100}, {-5, 100}, {7, 7}, {500, 500}, {501, 500}, {100000, 500}} {
		if _, err := r.svc.ListAccounts(ctx, staff, ports.UserQuery{Limit: c.asked}); err != nil {
			t.Fatal(err)
		}
		if got := r.store.LastUserQuery.Limit; got != c.want {
			t.Errorf("asked for %d, the store was asked for %d, want %d", c.asked, got, c.want)
		}
	}
	if _, err := r.svc.ListAccounts(ctx, staff, ports.UserQuery{Role: domain.RoleStaff, Status: "active", Search: "  Ada  "}); err != nil {
		t.Fatal(err)
	}
	if q := r.store.LastUserQuery; q.Role != domain.RoleStaff || q.Status != "active" || q.Search != "Ada" {
		t.Errorf("query = %+v", q)
	}
}

func TestAccountsAreListedNewestFirstAndCanBeFiltered(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	r.signedUp(t, "ada@example.com")
	time.Sleep(2 * time.Millisecond)
	r.signedUp(t, "cy@example.com")
	ada, _, _ := r.store.GetUserByEmail(ctx, "ada@example.com")
	if _, err := r.svc.SetDisabled(ctx, staff, ada.ID, true); err != nil {
		t.Fatal(err)
	}
	names := func(q ports.UserQuery) string {
		list, err := r.svc.ListAccounts(ctx, staff, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, u := range list {
			out = append(out, u.Email)
		}
		return strings.Join(out, ",")
	}
	if got := names(ports.UserQuery{}); got != "cy@example.com,ada@example.com,dee@example.com" && got != "cy@example.com,dee@example.com,ada@example.com" {
		t.Errorf("all = %q, want the newest first", got)
	}
	if got := names(ports.UserQuery{Role: domain.RoleStaff}); got != "dee@example.com" {
		t.Errorf("staff = %q", got)
	}
	if got := names(ports.UserQuery{Status: "disabled"}); got != "ada@example.com" {
		t.Errorf("disabled = %q", got)
	}
	if got := names(ports.UserQuery{Status: "active", Role: domain.RoleCustomer}); got != "cy@example.com" {
		t.Errorf("active customers = %q", got)
	}
	if got := names(ports.UserQuery{Search: "ADA@EXAMPLE"}); got != "ada@example.com" {
		t.Errorf("search = %q, want it to ignore case", got)
	}
	if got := names(ports.UserQuery{Limit: 1}); !strings.HasPrefix(got, "cy@") || strings.Contains(got, ",") {
		t.Errorf("limit 1 = %q", got)
	}
}

func TestAnAccountComesWithItsHistoryNewestFirst(t *testing.T) {
	r := newRig()
	staff := r.staffIn(t, "dee@example.com")
	ada := r.customerIn(t, "ada@example.com")
	r.svc.SetDisabled(ctx, staff, ada.User.ID, true)
	r.svc.SetDisabled(ctx, staff, ada.User.ID, false)

	user, events, err := r.svc.AccountDetail(ctx, staff, ada.User.ID)
	if err != nil || user.Email != "ada@example.com" || len(events) != 2 || events[0].Kind != domain.UserEnabled || events[1].Kind != domain.UserDisabled {
		t.Errorf("detail = %+v, %+v, %v", user, events, err)
	}
	r.store.AdminErr = errors.New("down")
	if _, _, err := r.svc.AccountDetail(ctx, staff, ada.User.ID); err == nil {
		t.Error("a failing history was not reported")
	}
}

// ---- passwords --------------------------------------------------------------------

func TestAGeneratedPasswordIsLongRandomAndWouldPassTheRules(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		pw, err := GeneratePassword()
		if err != nil || len(pw) != 24 {
			t.Fatalf("%q, %v", pw, err)
		}
		if why := ValidatePassword(pw, "a@b.co", "Name"); why != "" {
			t.Errorf("a generated password fails the rules: %s", why)
		}
		if seen[pw] {
			t.Fatalf("%q came twice", pw)
		}
		seen[pw] = true
	}
	withEntropy(t, 10) // fewer than the 18 it needs
	if _, err := GeneratePassword(); err == nil {
		t.Error("a password was made from too little randomness")
	}
}
