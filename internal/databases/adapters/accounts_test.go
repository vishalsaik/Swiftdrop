package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Managing other people's accounts, against the real database. Accounts made here have ids
// and emails starting with au- (see authDB), and their history goes with them.

func staffUser(t *testing.T, db *DBAdapter, name string) domain.User {
	t.Helper()
	return newUser(t, db, name, domain.RoleStaff)
}

// onlyOurStaff skips a test whose point is "the last staff account" when the database holds
// active staff accounts that these tests did not make.
func onlyOurStaff(t *testing.T, db *DBAdapter) {
	t.Helper()
	var others int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE role = 'staff' AND disabled_at IS NULL AND id NOT LIKE 'au-%'`).Scan(&others); err != nil {
		t.Fatal(err)
	}
	if others > 0 {
		t.Skipf("the database has %d active staff accounts that are not the tests' own", others)
	}
}

func eventKinds(t *testing.T, db *DBAdapter, userID string) string {
	t.Helper()
	ev, err := db.ListUserEvents(context.Background(), userID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(ev) - 1; i >= 0; i-- {
		out = append(out, ev[i].Kind)
	}
	return strings.Join(out, ",")
}

func actorOf(u domain.User) domain.User { return domain.User{ID: u.ID, Email: u.Email} }

// ---- listing ---------------------------------------------------------------------

func TestAccountsAreListedNewestFirstAndCanBeFiltered(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	older := newUser(t, db, "list-ada", domain.RoleCustomer)
	time.Sleep(5 * time.Millisecond)
	staff := staffUser(t, db, "list-dee")
	time.Sleep(5 * time.Millisecond)
	gone := newUser(t, db, "list-gone", domain.RoleCustomer)
	if _, _, err := db.SetUserDisabled(ctx, actorOf(staff), gone.ID, true); err != nil {
		t.Fatal(err)
	}

	emails := func(q ports.UserQuery) string {
		q.Search = firstNonEmpty(q.Search, "au-list")
		if q.Limit == 0 {
			q.Limit = 100
		}
		list, err := db.ListUsers(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, u := range list {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(u.Email, "au-"), "@example.test"))
		}
		return strings.Join(out, ",")
	}
	if got := emails(ports.UserQuery{}); got != "list-gone,list-dee,list-ada" {
		t.Errorf("all = %q, want the newest first", got)
	}
	if got := emails(ports.UserQuery{Role: domain.RoleStaff}); got != "list-dee" {
		t.Errorf("staff = %q", got)
	}
	if got := emails(ports.UserQuery{Status: "disabled"}); got != "list-gone" {
		t.Errorf("disabled = %q", got)
	}
	if got := emails(ports.UserQuery{Status: "active"}); got != "list-dee,list-ada" {
		t.Errorf("active = %q", got)
	}
	if got := emails(ports.UserQuery{Role: domain.RoleCustomer, Status: "active"}); got != "list-ada" {
		t.Errorf("active customers = %q", got)
	}
	if got := emails(ports.UserQuery{Limit: 2}); got != "list-gone,list-dee" {
		t.Errorf("limit 2 = %q", got)
	}
	if got := emails(ports.UserQuery{Search: "AU-LIST-ADA"}); got != "list-ada" {
		t.Errorf("search = %q, want it to ignore case", got)
	}
	if got := emails(ports.UserQuery{Search: "Test list-dee"}); got != "list-dee" { // the name, not the email
		t.Errorf("search by name = %q", got)
	}
	_ = older
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// What is typed in the search box is text, not a pattern: a % or _ matches itself.
func TestTheSearchIsTextNotAPattern(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	if _, err := db.CreateUser(ctx, domain.User{ID: "au-pct", Email: "au-pct@example.test", Name: "50% off", Role: domain.RoleCustomer}, "h"); err != nil {
		t.Fatal(err)
	}
	newUser(t, db, "plain", domain.RoleCustomer)
	list, err := db.ListUsers(ctx, ports.UserQuery{Search: "%", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var ours []string
	for _, u := range list {
		if strings.HasPrefix(u.ID, "au-") {
			ours = append(ours, u.ID)
		}
	}
	if len(ours) != 1 || ours[0] != "au-pct" {
		t.Errorf("a search for %% found %v, want only the account with a %% in its name", ours)
	}
	list, _ = db.ListUsers(ctx, ports.UserQuery{Search: "_", Limit: 100})
	for _, u := range list {
		if strings.HasPrefix(u.ID, "au-") && !strings.Contains(strings.ToLower(u.Email+u.Name), "_") {
			t.Errorf("a search for _ matched %s", u.ID)
		}
	}
}

func TestListingNeedsAPositiveLimitAndAKnownStatus(t *testing.T) {
	db := authDB(t)
	for _, q := range []ports.UserQuery{{Limit: 0}, {Limit: -1}, {Limit: 5, Status: "gone"}} {
		if got, err := db.ListUsers(context.Background(), q); err == nil || got != nil {
			t.Errorf("%+v: %v, %v, want an error", q, got, err)
		}
	}
}

// ---- making one ------------------------------------------------------------------

func TestAnAccountMadeByStaffIsStoredWithItsMarkAndItsHistory(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "mk-dee")

	made, err := db.CreateUserBy(ctx, actorOf(staff),
		domain.User{ID: "au-mk-new", Email: "au-mk-new@example.test", Name: "New", Role: domain.RoleStaff, MustChangePassword: true}, "hash-new")
	if err != nil || !made.MustChangePassword || made.Role != domain.RoleStaff {
		t.Fatalf("made = %+v, %v", made, err)
	}
	got, hash, err := db.GetUserByEmail(ctx, "au-mk-new@example.test")
	if err != nil || !got.MustChangePassword || hash != "hash-new" {
		t.Errorf("read back = %+v %q %v", got, hash, err)
	}
	ev, _ := db.ListUserEvents(ctx, made.ID, 10)
	if len(ev) != 1 || ev[0].Kind != domain.UserCreated || ev[0].ActorID != staff.ID || ev[0].ActorEmail != staff.Email || ev[0].Detail != "role=staff" || ev[0].UserID != made.ID {
		t.Errorf("history = %+v", ev)
	}
	// a sign-up made without a staff member says nothing must change
	signedUp := newUser(t, db, "mk-signup", domain.RoleCustomer)
	if signedUp.MustChangePassword {
		t.Error("an ordinary account must change its password")
	}
}

func TestAnEmailThatIsTakenMakesNothingAndWritesNothing(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "tk-dee")
	if _, err := db.CreateUserBy(ctx, actorOf(staff), domain.User{ID: "au-tk-a", Email: "au-tk@example.test", Name: "A", Role: domain.RoleCustomer}, "h"); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreateUserBy(ctx, actorOf(staff), domain.User{ID: "au-tk-b", Email: "au-tk@example.test", Name: "B", Role: domain.RoleCustomer}, "h")
	if !errors.Is(err, ports.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
	if _, err := db.GetUserByID(ctx, "au-tk-b"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Error("the second account exists")
	}
	var n int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE user_id = 'au-tk-b'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d history lines for an account that was never made", n)
	}
}

func TestMakingAnAccountNeedsWhoAndEveryPart(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "args-dee")
	ok := domain.User{ID: "au-args-x", Email: "au-args-x@example.test", Name: "X", Role: domain.RoleCustomer}
	for name, c := range map[string]struct {
		actor domain.User
		u     domain.User
		hash  string
	}{
		"no actor id":    {domain.User{Email: "e"}, ok, "h"},
		"no actor email": {domain.User{ID: "i"}, ok, "h"},
		"no id":          {actorOf(staff), domain.User{Email: ok.Email, Name: "X", Role: domain.RoleCustomer}, "h"},
		"no email":       {actorOf(staff), domain.User{ID: ok.ID, Name: "X", Role: domain.RoleCustomer}, "h"},
		"no name":        {actorOf(staff), domain.User{ID: ok.ID, Email: ok.Email, Role: domain.RoleCustomer}, "h"},
		"no hash":        {actorOf(staff), ok, ""},
		"a bad role":     {actorOf(staff), domain.User{ID: ok.ID, Email: ok.Email, Name: "X", Role: "admin"}, "h"},
	} {
		if _, err := db.CreateUserBy(ctx, c.actor, c.u, c.hash); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := db.GetUserByID(ctx, ok.ID); !errors.Is(err, ports.ErrUserNotFound) {
		t.Error("an account was made from incomplete input")
	}
}

// ---- switching off and on --------------------------------------------------------

func TestDisablingEndsEverySignInInTheSameTransactionAndIsRecorded(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "dis-dee")
	ada := newUser(t, db, "dis-ada", domain.RoleCustomer)
	now := time.Now().UTC()
	newSession(t, db, "au-dis-s1", ada.ID, now)
	newSession(t, db, "au-dis-s2", ada.ID, now)
	newSession(t, db, "au-dis-staff", staff.ID, now)

	user, changed, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, true)
	if err != nil || !changed || user.DisabledAt == nil || user.ID != ada.ID {
		t.Fatalf("SetUserDisabled = %+v, %v, %v", user, changed, err)
	}
	if got := sessionIDs(t, db, ada.ID); len(got) != 0 {
		t.Errorf("sessions left = %v, want none", got)
	}
	if got := sessionIDs(t, db, staff.ID); len(got) != 1 {
		t.Errorf("the staff member's own session was touched: %v", got)
	}
	ev, _ := db.ListUserEvents(ctx, ada.ID, 10)
	if len(ev) != 1 || ev[0].Kind != domain.UserDisabled || ev[0].ActorID != staff.ID || ev[0].ActorEmail != staff.Email {
		t.Errorf("history = %+v", ev)
	}
}

func TestEnablingBringsTheAccountBackAndDoingItTwiceWritesOneLine(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "en-dee")
	ada := newUser(t, db, "en-ada", domain.RoleCustomer)

	if _, changed, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, false); err != nil || changed { // already enabled
		t.Fatalf("enable an enabled account: changed=%v err=%v", changed, err)
	}
	if _, _, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, true); err != nil || changed { // already disabled
		t.Fatalf("disable a disabled account: changed=%v err=%v", changed, err)
	}
	user, changed, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, false)
	if err != nil || !changed || user.DisabledAt != nil {
		t.Fatalf("enable = %+v, %v, %v", user, changed, err)
	}
	if got := eventKinds(t, db, ada.ID); got != "disabled,enabled" {
		t.Errorf("history = %q, want one line for each real change", got)
	}
}

func TestOnlyAnotherPersonCanSwitchYouOff(t *testing.T) {
	db := authDB(t)
	staff := staffUser(t, db, "own-dee")
	staffUser(t, db, "own-spare")
	if _, _, err := db.SetUserDisabled(context.Background(), actorOf(staff), staff.ID, true); !errors.Is(err, ports.ErrOwnAccount) {
		t.Errorf("err = %v, want ErrOwnAccount even with another staff account to spare", err)
	}
	if got := eventKinds(t, db, staff.ID); got != "" {
		t.Errorf("history = %q", got)
	}
}

func TestAnAccountThatIsNotThereIsNotFound(t *testing.T) {
	db := authDB(t)
	staff := staffUser(t, db, "nf-dee")
	if _, _, err := db.SetUserDisabled(context.Background(), actorOf(staff), "au-ghost", true); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("disable: %v", err)
	}
	if _, err := db.ResetUserPassword(context.Background(), actorOf(staff), "au-ghost", "h"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("reset: %v", err)
	}
	if err := db.ChangePasswordHash(context.Background(), "au-ghost", "h"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("change: %v", err)
	}
}

func TestTheLastActiveStaffAccountCannotBeSwitchedOff(t *testing.T) {
	db := authDB(t)
	onlyOurStaff(t, db)
	ctx := context.Background()
	first := staffUser(t, db, "ls-first")
	someone := domain.User{ID: "au-ls-someone", Email: "au-ls-someone@example.test"} // an actor that is not either account

	if _, _, err := db.SetUserDisabled(ctx, someone, first.ID, true); !errors.Is(err, ports.ErrLastStaff) {
		t.Fatalf("err = %v, want ErrLastStaff for the only active staff account", err)
	}
	second := staffUser(t, db, "ls-second")
	if _, _, err := db.SetUserDisabled(ctx, someone, first.ID, true); err != nil {
		t.Fatalf("with a second one: %v", err)
	}
	if _, _, err := db.SetUserDisabled(ctx, someone, second.ID, true); !errors.Is(err, ports.ErrLastStaff) {
		t.Errorf("err = %v, want ErrLastStaff once only one is left", err)
	}
	// a customer is not staff: switching one off never counts
	cust := newUser(t, db, "ls-cust", domain.RoleCustomer)
	if _, _, err := db.SetUserDisabled(ctx, someone, cust.ID, true); err != nil {
		t.Errorf("customer: %v", err)
	}
	// and the refused one is still active
	if u, _ := db.GetUserByID(ctx, second.ID); u.Disabled() {
		t.Error("the last staff account was switched off")
	}
	// a switched-off staff account can always be switched back on
	if _, changed, err := db.SetUserDisabled(ctx, someone, first.ID, false); err != nil || !changed {
		t.Errorf("enable: changed=%v err=%v", changed, err)
	}
}

// Two staff members switching each other off at the same moment must not leave nobody. The
// active staff accounts are locked while the rule is checked, so they take turns: one is
// switched off, and the other is then the last and is refused.
func TestTwoStaffSwitchingEachOtherOffLeaveOneActive(t *testing.T) {
	db := authDB(t)
	onlyOurStaff(t, db)
	ctx := context.Background()
	a := staffUser(t, db, "race-a")
	b := staffUser(t, db, "race-b")

	for round := range 15 {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, pair := range [][2]domain.User{{a, b}, {b, a}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, errs[i] = db.SetUserDisabled(ctx, actorOf(pair[0]), pair[1].ID, true)
			}()
		}
		wg.Wait()

		wins, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ports.ErrLastStaff):
				refused++
			default:
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if wins != 1 || refused != 1 {
			t.Fatalf("round %d: %d switched off and %d refused, want exactly one of each", round, wins, refused)
		}
		var active int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id IN ($1, $2) AND disabled_at IS NULL`, a.ID, b.ID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != 1 {
			t.Fatalf("round %d: %d active staff accounts, want 1", round, active)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE users SET disabled_at = NULL WHERE id IN ($1, $2)`, a.ID, b.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTwoPeopleSwitchingTheSameAccountOffWriteOneLine(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	x := staffUser(t, db, "same-x")
	y := staffUser(t, db, "same-y")
	target := newUser(t, db, "same-target", domain.RoleCustomer)
	var wg sync.WaitGroup
	for _, actor := range []domain.User{x, y, x, y} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := db.SetUserDisabled(ctx, actorOf(actor), target.ID, true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := eventKinds(t, db, target.ID); got != "disabled" {
		t.Errorf("history = %q, want a single line", got)
	}
}

// ---- resetting a password --------------------------------------------------------

func TestResettingReplacesTheHashEndsSignInsAndMarksTheAccount(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "rs-dee")
	ada := newUser(t, db, "rs-ada", domain.RoleCustomer)
	newSession(t, db, "au-rs-s1", ada.ID, time.Now().UTC())

	user, err := db.ResetUserPassword(ctx, actorOf(staff), ada.ID, "temp-hash")
	if err != nil || !user.MustChangePassword {
		t.Fatalf("ResetUserPassword = %+v, %v", user, err)
	}
	_, hash, _ := db.GetUserByEmail(ctx, ada.Email)
	if hash != "temp-hash" {
		t.Errorf("hash = %q", hash)
	}
	if got := sessionIDs(t, db, ada.ID); len(got) != 0 {
		t.Errorf("sessions left = %v", got)
	}
	ev, _ := db.ListUserEvents(ctx, ada.ID, 10)
	if len(ev) != 1 || ev[0].Kind != domain.UserPasswordReset || ev[0].ActorID != staff.ID || ev[0].ActorEmail != staff.Email {
		t.Errorf("history = %+v", ev)
	}
}

func TestNobodyResetsTheirOwnPassword(t *testing.T) {
	db := authDB(t)
	staff := staffUser(t, db, "rs-own")
	if _, err := db.ResetUserPassword(context.Background(), actorOf(staff), staff.ID, "h"); !errors.Is(err, ports.ErrOwnAccount) {
		t.Errorf("err = %v", err)
	}
	_, hash, _ := db.GetUserByEmail(context.Background(), staff.Email)
	if hash != "hash-rs-own" {
		t.Errorf("hash = %q, want it unchanged", hash)
	}
}

func TestChoosingYourOwnPasswordClearsTheMarkAndIsRecordedAsTheirOwn(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "ch-dee")
	ada := newUser(t, db, "ch-ada", domain.RoleCustomer)
	if _, err := db.ResetUserPassword(ctx, actorOf(staff), ada.ID, "temp"); err != nil {
		t.Fatal(err)
	}
	if err := db.ChangePasswordHash(ctx, ada.ID, "mine"); err != nil {
		t.Fatal(err)
	}
	got, hash, _ := db.GetUserByEmail(ctx, ada.Email)
	if got.MustChangePassword || hash != "mine" {
		t.Errorf("account = %+v, hash %q", got, hash)
	}
	ev, _ := db.ListUserEvents(ctx, ada.ID, 10)
	if len(ev) != 2 || ev[0].Kind != domain.UserPasswordChanged || ev[0].ActorID != ada.ID || ev[0].ActorEmail != ada.Email {
		t.Errorf("history = %+v, want the account itself as the one who changed it", ev)
	}
	if err := db.ChangePasswordHash(ctx, ada.ID, ""); err == nil {
		t.Error("an empty hash was accepted")
	}
	if err := db.ChangePasswordHash(ctx, "", "h"); err == nil {
		t.Error("no account was accepted")
	}
}

func TestASessionShowsWhetherItsAccountMustChangeItsPassword(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "ms-dee")
	ada := newUser(t, db, "ms-ada", domain.RoleCustomer)
	newSession(t, db, "au-ms-s", ada.ID, time.Now().UTC())
	if _, err := db.ResetUserPassword(ctx, actorOf(staff), ada.ID, "temp"); err != nil {
		t.Fatal(err)
	}
	newSession(t, db, "au-ms-s2", ada.ID, time.Now().UTC())
	_, u, err := db.GetSession(ctx, "th-au-ms-s2")
	if err != nil || !u.MustChangePassword {
		t.Errorf("GetSession user = %+v, %v, want the mark", u, err)
	}
}

// ---- the history -----------------------------------------------------------------

func TestTheHistoryIsNewestFirstAndLimited(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "hi-dee")
	ada := newUser(t, db, "hi-ada", domain.RoleCustomer)
	for i := range 3 {
		if _, _, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	ev, err := db.ListUserEvents(ctx, ada.ID, 2)
	if err != nil || len(ev) != 2 || ev[0].Kind != domain.UserDisabled || ev[1].Kind != domain.UserEnabled || ev[0].ID <= ev[1].ID {
		t.Errorf("history = %+v, %v", ev, err)
	}
	empty, err := db.ListUserEvents(ctx, "au-nobody", 5)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty history = %v, %v, want an empty list, not nil", empty, err)
	}
	if _, err := db.ListUserEvents(ctx, ada.ID, 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}
}

func TestTheHistoryCanNeverBeChangedAndGoesWithItsAccount(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "ao-dee")
	ada := newUser(t, db, "ao-ada", domain.RoleCustomer)
	if _, _, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE user_events SET kind = 'enabled' WHERE user_id = $1`, ada.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("a history line was changed: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, ada.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM user_events WHERE user_id = $1`, ada.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d history lines outlived their account", n)
	}
}

// ---- all or nothing --------------------------------------------------------------

// A change and the line that says who made it are one transaction: if the line cannot be
// written, the change is undone. A constraint that refuses one kind of line stands in for a
// failing write.
func refuseHistoryOfKind(t *testing.T, db *DBAdapter, kind string) {
	t.Helper()
	ctx := context.Background()
	name := "au_refuse_" + kind
	if _, err := db.Pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE user_events ADD CONSTRAINT %s CHECK (kind <> '%s')`, name, kind)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), fmt.Sprintf(`ALTER TABLE user_events DROP CONSTRAINT IF EXISTS %s`, name))
	})
}

func TestADisableWhoseHistoryLineCannotBeWrittenIsUndone(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "tx-dee")
	ada := newUser(t, db, "tx-ada", domain.RoleCustomer)
	newSession(t, db, "au-tx-s1", ada.ID, time.Now().UTC())
	refuseHistoryOfKind(t, db, domain.UserDisabled)

	if _, _, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, true); err == nil {
		t.Fatal("the change went through without its history line")
	}
	if u, _ := db.GetUserByID(ctx, ada.ID); u.Disabled() {
		t.Error("the account was switched off although the transaction failed")
	}
	if got := sessionIDs(t, db, ada.ID); len(got) != 1 {
		t.Errorf("sessions = %v, want the sign-in kept: the whole change was undone", got)
	}
}

func TestAResetWhoseHistoryLineCannotBeWrittenIsUndone(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "tx2-dee")
	ada := newUser(t, db, "tx2-ada", domain.RoleCustomer)
	newSession(t, db, "au-tx2-s1", ada.ID, time.Now().UTC())
	refuseHistoryOfKind(t, db, domain.UserPasswordReset)

	if _, err := db.ResetUserPassword(ctx, actorOf(staff), ada.ID, "temp"); err == nil {
		t.Fatal("the reset went through without its history line")
	}
	got, hash, _ := db.GetUserByEmail(ctx, ada.Email)
	if hash != "hash-tx2-ada" || got.MustChangePassword {
		t.Errorf("account = %+v, hash %q: the reset must be undone", got, hash)
	}
	if ids := sessionIDs(t, db, ada.ID); len(ids) != 1 {
		t.Errorf("sessions = %v, want the sign-in kept", ids)
	}
}

func TestACreationWhoseHistoryLineCannotBeWrittenMakesNoAccount(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "tx3-dee")
	refuseHistoryOfKind(t, db, domain.UserCreated)
	if _, err := db.CreateUserBy(ctx, actorOf(staff), domain.User{ID: "au-tx3-new", Email: "au-tx3-new@example.test", Name: "N", Role: domain.RoleCustomer}, "h"); err == nil {
		t.Fatal("the account was made without its history line")
	}
	if _, err := db.GetUserByID(ctx, "au-tx3-new"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Error("the account exists")
	}
}

func TestAPasswordChangeWhoseHistoryLineCannotBeWrittenIsUndone(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ada := newUser(t, db, "tx4-ada", domain.RoleCustomer)
	refuseHistoryOfKind(t, db, domain.UserPasswordChanged)
	if err := db.ChangePasswordHash(ctx, ada.ID, "mine"); err == nil {
		t.Fatal("the change went through without its history line")
	}
	if _, hash, _ := db.GetUserByEmail(ctx, ada.Email); hash != "hash-tx4-ada" {
		t.Errorf("hash = %q, want it unchanged", hash)
	}
}

func TestAccountManagementFailuresAreWrappedAndNeverLookLikeNotFound(t *testing.T) {
	db := authDB(t)
	staff := staffUser(t, db, "fail-dee")
	ada := newUser(t, db, "fail-ada", domain.RoleCustomer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ListUsers(ctx, ports.UserQuery{Limit: 5}); err == nil {
		t.Error("ListUsers: no error")
	}
	if _, err := db.CreateUserBy(ctx, actorOf(staff), domain.User{ID: "au-fail-n", Email: "au-fail-n@example.test", Name: "N", Role: domain.RoleCustomer}, "h"); err == nil || errors.Is(err, ports.ErrEmailTaken) {
		t.Errorf("CreateUserBy: %v", err)
	}
	if _, _, err := db.SetUserDisabled(ctx, actorOf(staff), ada.ID, true); err == nil || errors.Is(err, ports.ErrUserNotFound) || errors.Is(err, ports.ErrLastStaff) {
		t.Errorf("SetUserDisabled: %v", err)
	}
	if _, err := db.ResetUserPassword(ctx, actorOf(staff), ada.ID, "h"); err == nil || errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("ResetUserPassword: %v", err)
	}
	if err := db.ChangePasswordHash(ctx, ada.ID, "h"); err == nil || errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("ChangePasswordHash: %v", err)
	}
	if _, err := db.ListUserEvents(ctx, ada.ID, 5); err == nil {
		t.Error("ListUserEvents: no error")
	}
}

func TestChangingAnAccountNeedsWhoAndWhich(t *testing.T) {
	db := authDB(t)
	staff := staffUser(t, db, "need-dee")
	ctx := context.Background()
	for name, f := range map[string]func() error{
		"disable, no actor": func() error { _, _, err := db.SetUserDisabled(ctx, domain.User{}, "x", true); return err },
		"disable, no id":    func() error { _, _, err := db.SetUserDisabled(ctx, actorOf(staff), "", true); return err },
		"reset, no actor":   func() error { _, err := db.ResetUserPassword(ctx, domain.User{}, "x", "h"); return err },
		"reset, no id":      func() error { _, err := db.ResetUserPassword(ctx, actorOf(staff), "", "h"); return err },
		"reset, no hash":    func() error { _, err := db.ResetUserPassword(ctx, actorOf(staff), "x", ""); return err },
	} {
		if err := f(); err == nil || errors.Is(err, ports.ErrUserNotFound) {
			t.Errorf("%s: %v, want an input error", name, err)
		}
	}
}
