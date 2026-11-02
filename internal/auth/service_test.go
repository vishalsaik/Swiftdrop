package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/auth/authtest"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	svc   *Service
	store *authtest.Store
	clock *clock
}

func newRig() *rig {
	st := authtest.New()
	c := &clock{t: t0}
	s := New(st)
	s.Params = cheap
	s.Now = c.now
	return &rig{svc: s, store: st, clock: c}
}

const goodPassword = "a long enough phrase"

// signedUp makes a customer and returns their email.
func (r *rig) signedUp(t *testing.T, email string) {
	t.Helper()
	if _, err := r.svc.SignUp(context.Background(), "9.9.9.9", email, "Ada Lovelace", goodPassword); err != nil {
		t.Fatalf("SignUp(%s): %v", email, err)
	}
}

func (r *rig) signedIn(t *testing.T, email string) (string, Principal) {
	t.Helper()
	token, p, err := r.svc.SignIn(context.Background(), "1.1.1.1", email, goodPassword)
	if err != nil {
		t.Fatalf("SignIn(%s): %v", email, err)
	}
	return token, p
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

var ctx = context.Background()

// ---------------------------------------------------------------------------
// accounts
// ---------------------------------------------------------------------------

func TestSigningUpMakesACustomerWhoseEmailIsNormalisedAndPasswordHashed(t *testing.T) {
	r := newRig()
	u, err := r.svc.SignUp(ctx, "9.9.9.9", "  Ada@Example.COM ", "  Ada Lovelace ", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "ada@example.com" || u.Name != "Ada Lovelace" || u.Role != domain.RoleCustomer || u.ID == "" {
		t.Errorf("user = %+v", u)
	}
	stored := r.store.Users["ada@example.com"]
	if stored == nil || strings.Contains(stored.Hash, goodPassword) || !strings.HasPrefix(stored.Hash, "$argon2id$") {
		t.Errorf("stored hash = %v, want an argon2id hash and never the password", stored)
	}
}

func TestOnlyTheStaffCommandMakesStaff(t *testing.T) {
	r := newRig()
	u, err := r.svc.CreateStaff(ctx, "dispatch@example.com", "Dee Patch", goodPassword)
	if err != nil || u.Role != domain.RoleStaff {
		t.Errorf("CreateStaff = %+v, %v, want a staff account", u, err)
	}
	if got, _ := r.svc.SignUp(ctx, "9.9.9.9", "cust@example.com", "Cu Stomer", goodPassword); got.Role != domain.RoleCustomer {
		t.Errorf("a web sign-up got role %q", got.Role)
	}
}

func TestSigningUpNamesEveryFieldThatIsWrong(t *testing.T) {
	r := newRig()
	_, err := r.svc.SignUp(ctx, "9.9.9.9", "not an email", "   ", "short")
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("error = %v, want an InvalidError", err)
	}
	for _, f := range []string{"email", "name", "password"} {
		if inv.Fields[f] == "" {
			t.Errorf("field %q was not reported: %v", f, inv.Fields)
		}
	}
	if !strings.Contains(inv.Error(), "email") {
		t.Errorf("message = %q", inv.Error())
	}
	if len(r.store.Users) != 0 {
		t.Error("an account was made from invalid input")
	}
}

func TestEmailAndNameRules(t *testing.T) {
	for email, ok := range map[string]bool{
		"a@b.co": true, "first.last+tag@sub.example.org": true,
		"": false, "a@b": false, "@b.co": false, "a@": false, "a b@c.co": false,
		"Ada <a@b.co>": false, "a@b.co.": false, "a@@b.co": false,
		strings.Repeat("a", 250) + "@b.co": false,
	} {
		if got := validEmail(email); got != ok {
			t.Errorf("validEmail(%q) = %v, want %v", email, got, ok)
		}
	}
	if validName("") == "" || validName(strings.Repeat("x", 81)) == "" || validName("tab\there") == "" {
		t.Error("an empty, over-long or control-character name was accepted")
	}
	if validName("Zoë Ångström-Ñ") != "" {
		t.Error("a name with accents was refused")
	}
	if NormalizeEmail("  A@B.Co ") != "a@b.co" {
		t.Error("NormalizeEmail did not trim and lower-case")
	}
}

func TestAnEmailThatIsTakenIsSaidSoAndABrokenStoreIsPassedOn(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	if _, err := r.svc.SignUp(ctx, "9.9.9.9", "ADA@example.com", "Other Ada", goodPassword); !errors.Is(err, ports.ErrEmailTaken) {
		t.Errorf("error = %v, want ErrEmailTaken", err)
	}
	r.store.CreateUserErr = errors.New("connection refused")
	if _, err := r.svc.SignUp(ctx, "9.9.9.9", "new@example.com", "New Person", goodPassword); err == nil || errors.Is(err, ports.ErrEmailTaken) {
		t.Errorf("error = %v, want the store's failure", err)
	}
}

func TestSignUpsFromOneAddressAreLimited(t *testing.T) {
	r := newRig()
	for i := range 10 {
		if _, err := r.svc.SignUp(ctx, "5.5.5.5", fmt.Sprintf("user%d@example.com", i), "A Person", goodPassword); err != nil {
			t.Fatalf("sign-up %d: %v", i, err)
		}
	}
	_, err := r.svc.SignUp(ctx, "5.5.5.5", "one-more@example.com", "A Person", goodPassword)
	var many *TooManyAttemptsError
	if !errors.As(err, &many) || many.RetryAfter <= 0 || many.RetryAfter > time.Hour {
		t.Fatalf("error = %v, want TooManyAttempts with a wait of up to an hour", err)
	}
	if !strings.Contains(many.Error(), "try again in") {
		t.Errorf("message = %q", many.Error())
	}
	if _, err := r.svc.SignUp(ctx, "6.6.6.6", "someone@example.com", "A Person", goodPassword); err != nil {
		t.Errorf("another address: %v", err)
	}
	r.clock.advance(time.Hour)
	if _, err := r.svc.SignUp(ctx, "5.5.5.5", "later@example.com", "A Person", goodPassword); err != nil {
		t.Errorf("an hour later: %v", err)
	}
}

// ---------------------------------------------------------------------------
// signing in
// ---------------------------------------------------------------------------

func TestSigningInGivesATokenThatIsNeverStoredAndIdentifiesTheUser(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	token, p := r.signedIn(t, " ADA@example.com")
	if len(token) != tokenLength {
		t.Errorf("token length = %d, want %d", len(token), tokenLength)
	}
	if p.User.Email != "ada@example.com" || p.SessionID == "" || len(p.CSRFToken) != tokenLength {
		t.Errorf("principal = %+v", p)
	}
	for hash, s := range r.store.Sessions {
		if hash == token || s.TokenHash == token || strings.Contains(hash, token) {
			t.Error("the token itself is stored: only its hash may be")
		}
	}
	got, err := r.svc.Authenticate(ctx, token)
	if err != nil || got.User.ID != p.User.ID || got.SessionID != p.SessionID || got.CSRFToken != p.CSRFToken {
		t.Errorf("Authenticate = %+v, %v, want the same principal", got, err)
	}
	tok2, _ := r.signedIn(t, "ada@example.com")
	if tok2 == token {
		t.Error("two sign-ins got the same token")
	}
}

func TestEveryFailedSignInSaysTheSameThing(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	r.signedUp(t, "off@example.com")
	disabled := r.store.Users["off@example.com"]
	now := t0
	disabled.User.DisabledAt = &now

	for name, c := range map[string]struct{ email, password string }{
		"wrong password":                     {"ada@example.com", "not the password at all"},
		"unknown email":                      {"nobody@example.com", goodPassword},
		"not even an email":                  {"@@", goodPassword},
		"a disabled account, right password": {"off@example.com", goodPassword},
	} {
		_, _, err := r.svc.SignIn(ctx, "1.1.1.1", c.email, c.password)
		if !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%s: error = %v, want ErrBadCredentials", name, err)
		}
	}
	if r.store.SessionCount() != 0 {
		t.Error("a failed sign-in started a session")
	}
}

func TestFiveWrongPasswordsLockAnEmailForFifteenMinutesEvenForTheRightOne(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	for range 5 {
		if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", "wrong password here"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("error = %v", err)
		}
	}
	_, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword)
	var many *TooManyAttemptsError
	if !errors.As(err, &many) {
		t.Fatalf("error = %v, want TooManyAttempts: the right password must not work while locked", err)
	}
	if many.RetryAfter <= 14*time.Minute || many.RetryAfter > 15*time.Minute {
		t.Errorf("retry after = %v, want just under 15 minutes", many.RetryAfter)
	}
	// someone else on another address is not locked out by it
	r.signedUp(t, "bob@example.com")
	if _, _, err := r.svc.SignIn(ctx, "2.2.2.2", "bob@example.com", goodPassword); err != nil {
		t.Errorf("another account: %v", err)
	}
	r.clock.advance(15 * time.Minute)
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err != nil {
		t.Errorf("after the wait: %v", err)
	}
}

func TestASuccessfulSignInClearsTheFailuresBefore(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	for range 4 {
		r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", "wrong password here")
	}
	r.signedIn(t, "ada@example.com")
	for range 4 { // four more would be locked out if the first four still counted
		if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", "wrong password here"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("error = %v, want a plain failure", err)
		}
	}
}

func TestThirtyFailuresFromOneAddressLockThatAddressForEveryAccount(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	for i := range 30 {
		r.svc.SignIn(ctx, "7.7.7.7", fmt.Sprintf("guess%d@example.com", i), "wrong password here")
	}
	_, _, err := r.svc.SignIn(ctx, "7.7.7.7", "ada@example.com", goodPassword)
	var many *TooManyAttemptsError
	if !errors.As(err, &many) {
		t.Errorf("error = %v, want the address locked", err)
	}
	if _, _, err := r.svc.SignIn(ctx, "8.8.8.8", "ada@example.com", goodPassword); err != nil {
		t.Errorf("another address: %v", err)
	}
}

func TestAWeakOldHashIsReplacedAtSignInAndAFailureToDoSoIsOnlyLogged(t *testing.T) {
	logs := captureLog(t)
	r := newRig()
	weak := Params{Time: 1, MemoryKiB: 8, Threads: 1, KeyLen: 16, SaltLen: 8}
	h, _ := Hash(goodPassword, weak)
	r.store.Users["ada@example.com"] = &authtest.Stored{User: domain.User{ID: "u1", Email: "ada@example.com", Name: "Ada", Role: domain.RoleCustomer}, Hash: h}

	r.signedIn(t, "ada@example.com")
	if got := r.store.Users["ada@example.com"].Hash; got == h || !strings.Contains(got, "m=64,") {
		t.Errorf("hash = %q, want it replaced with the current cost", got)
	}

	r.store.Users["ada@example.com"].Hash = h
	r.store.UpdateErr = errors.New("connection refused")
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err != nil {
		t.Errorf("a failed upgrade stopped the sign-in: %v", err)
	}
	if !strings.Contains(logs.String(), "upgrading the password hash") {
		t.Errorf("the failure was not logged:\n%s", logs)
	}
}

func TestAHashWeCannotReadIsAFailedSignInNotACrash(t *testing.T) {
	r := newRig()
	r.store.Users["ada@example.com"] = &authtest.Stored{User: domain.User{ID: "u1", Email: "ada@example.com", Role: domain.RoleCustomer}, Hash: "garbage"}
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("error = %v, want ErrBadCredentials", err)
	}
}

func TestStoreFailuresWhileSigningInArePassedOnAndNeverLookLikeABadPassword(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	r.store.GetUserErr = errors.New("connection refused")
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err == nil || errors.Is(err, ErrBadCredentials) {
		t.Errorf("error = %v, want the store's failure", err)
	}
	r.store.GetUserErr = nil
	r.store.CreateSessionErr = errors.New("connection refused")
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err == nil || errors.Is(err, ErrBadCredentials) {
		t.Errorf("error = %v, want the store's failure", err)
	}
}

func TestSigningInGivesUpWhenTheCallerDoesWhileWaitingToHash(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	for range cap(r.svc.slots) { // every hashing slot is busy
		r.svc.slots <- struct{}{}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := r.svc.SignIn(cancelled, "1.1.1.1", "ada@example.com", goodPassword); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation", err)
	}
	if _, err := r.svc.SignUp(cancelled, "3.3.3.3", "new@example.com", "New Person", goodPassword); !errors.Is(err, context.Canceled) {
		t.Errorf("sign-up error = %v, want the cancellation", err)
	}
}

// ---------------------------------------------------------------------------
// who a request is
// ---------------------------------------------------------------------------

func TestATokenThatIsNotASessionIsNotSignedIn(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	r.signedIn(t, "ada@example.com")
	for name, token := range map[string]string{
		"empty": "", "too short": "abc", "too long": strings.Repeat("a", tokenLength+1),
		"the right length, never issued": strings.Repeat("a", tokenLength),
	} {
		if _, err := r.svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: error = %v, want ErrUnauthenticated", name, err)
		}
	}
}

func TestASessionEndsWhenItsLifetimeOrItsIdleTimeIsUp(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")

	// idle: unused for a day
	token, _ := r.signedIn(t, "ada@example.com")
	r.clock.advance(DefaultIdleTimeout - time.Second)
	if _, err := r.svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("just inside the idle time: %v", err)
	}
	r.clock.advance(DefaultIdleTimeout) // unused for a whole idle period since that use
	if _, err := r.svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("after the idle time: %v, want ErrUnauthenticated", err)
	}
	if r.store.SessionCount() != 0 {
		t.Error("an ended session was left in the store")
	}

	// absolute: used every few hours, but 14 days is the most
	token, _ = r.signedIn(t, "ada@example.com")
	for range 13 * 4 { // 13 days of use every 6 hours
		r.clock.advance(6 * time.Hour)
		if _, err := r.svc.Authenticate(ctx, token); err != nil {
			t.Fatalf("an active session ended early: %v", err)
		}
	}
	r.clock.advance(48 * time.Hour)
	if _, err := r.svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("past 14 days: %v, want ErrUnauthenticated however active", err)
	}
}

func TestADisabledAccountsSessionStopsWorking(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	token, _ := r.signedIn(t, "ada@example.com")
	now := r.clock.now()
	r.store.Users["ada@example.com"].User.DisabledAt = &now
	if _, err := r.svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("error = %v, want ErrUnauthenticated", err)
	}
}

func TestLastSeenIsWrittenAtMostOncePerMinute(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	token, _ := r.signedIn(t, "ada@example.com")
	for range 20 {
		r.clock.advance(2 * time.Second)
		r.svc.Authenticate(ctx, token)
	}
	if r.store.Touches != 0 {
		t.Errorf("%d writes within a minute, want none", r.store.Touches)
	}
	r.clock.advance(time.Minute)
	r.svc.Authenticate(ctx, token)
	if r.store.Touches != 1 {
		t.Errorf("%d writes after a minute, want 1", r.store.Touches)
	}
}

func TestStoreFailuresWhileCheckingASessionAreNotASignOut(t *testing.T) {
	logs := captureLog(t)
	r := newRig()
	r.signedUp(t, "ada@example.com")
	token, _ := r.signedIn(t, "ada@example.com")

	r.store.GetSessionErr = errors.New("connection refused")
	if _, err := r.svc.Authenticate(ctx, token); err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("error = %v, want the store's failure, not 'signed out'", err)
	}
	r.store.GetSessionErr = nil

	r.clock.advance(time.Hour)
	r.store.TouchErr = errors.New("connection refused")
	if _, err := r.svc.Authenticate(ctx, token); err != nil {
		t.Errorf("a failed 'last seen' write signed the person out: %v", err)
	}
	r.store.TouchErr = nil

	r.clock.advance(DefaultSessionLifetime)
	r.store.DeleteSessionErr = errors.New("connection refused")
	if _, err := r.svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("error = %v, want ErrUnauthenticated", err)
	}
	for _, want := range []string{"reading", "recording that a session was used", "removing an ended session"} {
		if want == "reading" {
			continue // returned, not logged
		}
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
}

func TestSigningOutEndsOnlyThatSession(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	laptop, p := r.signedIn(t, "ada@example.com")
	phone, _ := r.signedIn(t, "ada@example.com")
	if err := r.svc.SignOut(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Authenticate(ctx, laptop); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("the session signed out still works: %v", err)
	}
	if _, err := r.svc.Authenticate(ctx, phone); err != nil {
		t.Errorf("the other session was ended too: %v", err)
	}
	if err := r.svc.SignOut(ctx, p); err != nil {
		t.Errorf("signing out twice: %v", err)
	}
}

// ---------------------------------------------------------------------------
// changing the password
// ---------------------------------------------------------------------------

func TestChangingThePasswordEndsEveryOtherSignInButNotThisOne(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	here, p := r.signedIn(t, "ada@example.com")
	elsewhere, _ := r.signedIn(t, "ada@example.com")

	if err := r.svc.ChangePassword(ctx, p, goodPassword, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Authenticate(ctx, here); err != nil {
		t.Errorf("this session was ended: %v", err)
	}
	if _, err := r.svc.Authenticate(ctx, elsewhere); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("another session survived a password change: %v", err)
	}
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("the old password still works: %v", err)
	}
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", "a brand new passphrase"); err != nil {
		t.Errorf("the new password: %v", err)
	}
}

func TestChangingThePasswordNeedsTheRightOneAndAGoodNewOne(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	_, p := r.signedIn(t, "ada@example.com")

	if err := r.svc.ChangePassword(ctx, p, "not the current one", "a brand new passphrase"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("error = %v, want ErrWrongPassword", err)
	}
	var inv *InvalidError
	if err := r.svc.ChangePassword(ctx, p, goodPassword, "short"); !errors.As(err, &inv) || inv.Fields["newPassword"] == "" {
		t.Errorf("error = %v, want the new password refused", err)
	}
	if err := r.svc.ChangePassword(ctx, p, goodPassword, goodPassword); !errors.As(err, &inv) || !strings.Contains(inv.Fields["newPassword"], "different") {
		t.Errorf("error = %v, want 'must be different'", err)
	}
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err != nil {
		t.Errorf("a refused change altered the password: %v", err)
	}
}

func TestGuessingTheCurrentPasswordIsLimited(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	_, p := r.signedIn(t, "ada@example.com")
	for range 5 {
		r.svc.ChangePassword(ctx, p, "a wrong guess here", "a brand new passphrase")
	}
	var many *TooManyAttemptsError
	if err := r.svc.ChangePassword(ctx, p, goodPassword, "a brand new passphrase"); !errors.As(err, &many) {
		t.Errorf("error = %v, want TooManyAttempts: even the right password must wait", err)
	}
}

func TestStoreFailuresWhileChangingThePasswordArePassedOn(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	_, p := r.signedIn(t, "ada@example.com")
	boom := errors.New("connection refused")

	r.store.GetUserErr = boom
	if err := r.svc.ChangePassword(ctx, p, goodPassword, "a brand new passphrase"); !errors.Is(err, boom) {
		t.Errorf("reading the account: %v", err)
	}
	r.store.GetUserErr = nil
	r.store.UpdateErr = boom
	if err := r.svc.ChangePassword(ctx, p, goodPassword, "a brand new passphrase"); !errors.Is(err, boom) {
		t.Errorf("saving: %v", err)
	}
	r.store.UpdateErr = nil
	r.store.DeleteUserSessionsErr = boom
	if err := r.svc.ChangePassword(ctx, p, goodPassword, "a brand new passphrase"); !errors.Is(err, boom) {
		t.Errorf("ending the other sign-ins: %v", err)
	}
	// a hash we cannot read is an error, not "wrong password"
	r.store.DeleteUserSessionsErr = nil
	r.store.Users["ada@example.com"].Hash = "garbage"
	if err := r.svc.ChangePassword(ctx, p, goodPassword, "a brand new passphrase"); err == nil || errors.Is(err, ErrWrongPassword) {
		t.Errorf("an unreadable hash: %v", err)
	}
}

// ---------------------------------------------------------------------------
// housekeeping and helpers
// ---------------------------------------------------------------------------

func TestOldSessionsAreCleanedUp(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	r.signedIn(t, "ada@example.com")
	r.clock.advance(DefaultIdleTimeout + time.Hour)
	r.signedIn(t, "ada@example.com") // a fresh one
	n, err := r.svc.Cleanup(ctx)
	if err != nil || n != 1 || r.store.SessionCount() != 1 {
		t.Errorf("removed %d (%v), %d left, want 1 removed and 1 left", n, err, r.store.SessionCount())
	}
}

func TestTheCleanupLoopRunsNowAndOnEveryTickAndStops(t *testing.T) {
	logs := captureLog(t)
	r := newRig()
	r.signedUp(t, "ada@example.com")
	r.signedIn(t, "ada@example.com")
	r.clock.advance(DefaultSessionLifetime + time.Hour)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.svc.RunCleanup(runCtx, 5*time.Millisecond); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for r.store.SessionCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if r.store.SessionCount() != 0 {
		t.Error("the loop did not remove the old session")
	}
	r.store.FailDeleteExpired(errors.New("connection refused"))
	for !strings.Contains(logs.String(), "removing old sessions") && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not stop")
	}
	for _, want := range []string{"removed 1 old sessions", "removing old sessions"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	r.svc.RunCleanup(ctx, 0) // a zero interval returns at once
}

func TestTokensAreHashedConsistentlyAndLookRandom(t *testing.T) {
	first, again, other := HashToken("abc"), HashToken("abc"), HashToken("abd")
	if first != again || first == other || len(first) != 64 {
		t.Error("HashToken is not a stable 64-character digest")
	}
	seen := map[string]bool{}
	for range 200 {
		tok, err := randomToken()
		if err != nil || len(tok) != tokenLength || seen[tok] {
			t.Fatalf("token %q, err %v: wrong length or repeated", tok, err)
		}
		seen[tok] = true
	}
}

// ---------------------------------------------------------------------------
// when the system's source of randomness fails
// ---------------------------------------------------------------------------

// failingReader works for the first n bytes it is asked for, then fails.
type failingReader struct{ left int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errors.New("no entropy")
	}
	n := min(len(p), f.left)
	f.left -= n
	return n, nil
}

func withEntropy(t *testing.T, readable int) {
	t.Helper()
	old := entropy
	entropy = &failingReader{left: readable}
	t.Cleanup(func() { entropy = old })
}

func TestWithoutRandomnessNothingIsHashedOrIssued(t *testing.T) {
	withEntropy(t, 0)
	if _, err := Hash("some password here", cheap); err == nil {
		t.Error("a hash was made with no randomness for the salt")
	}
	if _, err := randomToken(); err == nil {
		t.Error("a token was made with no randomness")
	}
}

func TestEachStepOfSigningInStopsIfRandomnessFails(t *testing.T) {
	for name, readable := range map[string]int{
		"no session token": 0,
		"no CSRF token":    tokenBytes, // the first token is made, the second cannot be
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			r.signedUp(t, "ada@example.com")
			withEntropy(t, readable)
			if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err == nil || errors.Is(err, ErrBadCredentials) {
				t.Errorf("error = %v, want the randomness failure", err)
			}
			if r.store.SessionCount() != 0 {
				t.Error("a session was started anyway")
			}
		})
	}
}

func TestAStandInHashThatCannotBeMadeStillGivesTheSameFailedSignIn(t *testing.T) {
	r := newRig()
	withEntropy(t, 0)
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "nobody@example.com", goodPassword); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("error = %v, want ErrBadCredentials", err)
	}
}

func TestAPasswordChangeStopsIfTheNewHashCannotBeMade(t *testing.T) {
	r := newRig()
	r.signedUp(t, "ada@example.com")
	_, p := r.signedIn(t, "ada@example.com")
	withEntropy(t, 0)
	if err := r.svc.ChangePassword(ctx, p, goodPassword, "a brand new passphrase"); err == nil || errors.Is(err, ErrWrongPassword) {
		t.Errorf("error = %v, want the randomness failure", err)
	}
	// and the old password still works
	withEntropy(t, 1<<20)
	if _, _, err := r.svc.SignIn(ctx, "1.1.1.1", "ada@example.com", goodPassword); err != nil {
		t.Errorf("the old password after a failed change: %v", err)
	}
}
