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

// Accounts, sign-ins and "my orders" against the real database. Everything these
// tests make has an id or an email starting with au-, and is removed at the end.

func authDB(t *testing.T) *DBAdapter {
	t.Helper()
	db := testAdapter(t)
	clean := func() {
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, "DELETE FROM users WHERE id LIKE 'au-%'") // sessions go with them
		_, _ = db.Pool.Exec(ctx, "DELETE FROM orders WHERE customer_id LIKE 'au-%'")
	}
	clean()
	t.Cleanup(clean)
	return db
}

func newUser(t *testing.T, db *DBAdapter, name string, role domain.Role) domain.User {
	t.Helper()
	u, err := db.CreateUser(context.Background(),
		domain.User{ID: "au-" + name, Email: "au-" + name + "@example.test", Name: "Test " + name, Role: role}, "hash-"+name)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u
}

func newSession(t *testing.T, db *DBAdapter, id, userID string, now time.Time) domain.Session {
	t.Helper()
	s := domain.Session{
		ID: id, UserID: userID, TokenHash: "th-" + id, CSRFToken: "csrf-" + id,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour),
	}
	if err := db.CreateSession(context.Background(), s); err != nil {
		t.Fatalf("CreateSession(%s): %v", id, err)
	}
	return s
}

func sessionIDs(t *testing.T, db *DBAdapter, userID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), "SELECT id FROM sessions WHERE user_id = $1 ORDER BY id", userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// ---------------------------------------------------------------------------
// accounts
// ---------------------------------------------------------------------------

func TestAnAccountIsStoredAndReadBackByEmailAndId(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	created := newUser(t, db, "ada", domain.RoleCustomer)
	if created.CreatedAt.IsZero() || created.DisabledAt != nil || created.Role != domain.RoleCustomer {
		t.Errorf("created = %+v", created)
	}
	u, hash, err := db.GetUserByEmail(ctx, "au-ada@example.test")
	if err != nil || u.ID != "au-ada" || hash != "hash-ada" || u.Name != "Test ada" {
		t.Errorf("GetUserByEmail = %+v, %q, %v", u, hash, err)
	}
	byID, err := db.GetUserByID(ctx, "au-ada")
	if err != nil || byID.Email != "au-ada@example.test" {
		t.Errorf("GetUserByID = %+v, %v", byID, err)
	}
	if _, _, err := db.GetUserByEmail(ctx, "au-nobody@example.test"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("unknown email: %v", err)
	}
	if _, err := db.GetUserByID(ctx, "au-nobody"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestTwoAccountsCannotShareAnEmail(t *testing.T) {
	db := authDB(t)
	newUser(t, db, "ada", domain.RoleCustomer)
	_, err := db.CreateUser(context.Background(),
		domain.User{ID: "au-ada2", Email: "au-ada@example.test", Name: "Other", Role: domain.RoleStaff}, "h")
	if !errors.Is(err, ports.ErrEmailTaken) {
		t.Errorf("error = %v, want ErrEmailTaken", err)
	}
}

// Twenty people sign up with the same address at once: the database picks one.
func TestTwentySimultaneousSignUpsForOneEmailHaveExactlyOneWinner(t *testing.T) {
	db := authDB(t)
	const n = 20
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			_, errs[i] = db.CreateUser(context.Background(),
				domain.User{ID: fmt.Sprintf("au-race-%d", i), Email: "au-race@example.test", Name: "Racer", Role: domain.RoleCustomer}, "h")
		})
	}
	close(start)
	wg.Wait()
	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, ports.ErrEmailTaken):
			t.Errorf("sign-up %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 {
		t.Errorf("%d sign-ups won, want exactly 1", wins)
	}
}

func TestTheDatabaseItselfRefusesAMixedCaseEmailOrAnUnknownRole(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users (id, email, name, role, password_hash) VALUES ('au-up', 'au-Up@example.test', 'x', 'customer', 'h')`); err == nil {
		t.Error("a mixed-case email was stored: two spellings could become two accounts")
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users (id, email, name, role, password_hash) VALUES ('au-adm', 'au-adm@example.test', 'x', 'admin', 'h')`); err == nil {
		t.Error("an unknown role was stored")
	}
}

func TestCreatingAnAccountNeedsEveryPart(t *testing.T) {
	db := authDB(t)
	ok := domain.User{ID: "au-x", Email: "au-x@example.test", Name: "X", Role: domain.RoleCustomer}
	for name, mutate := range map[string]func(u *domain.User) string{
		"no id":    func(u *domain.User) string { u.ID = ""; return "h" },
		"no email": func(u *domain.User) string { u.Email = ""; return "h" },
		"no name":  func(u *domain.User) string { u.Name = ""; return "h" },
		"no role":  func(u *domain.User) string { u.Role = ""; return "h" },
		"bad role": func(u *domain.User) string { u.Role = "admin"; return "h" },
		"no hash":  func(u *domain.User) string { return "" },
	} {
		u := ok
		hash := mutate(&u)
		if _, err := db.CreateUser(context.Background(), u, hash); err == nil || errors.Is(err, ports.ErrEmailTaken) {
			t.Errorf("%s: error = %v, want a refusal", name, err)
		}
	}
}

func TestAPasswordHashCanBeReplaced(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	newUser(t, db, "ada", domain.RoleCustomer)
	if err := db.UpdatePasswordHash(ctx, "au-ada", "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, hash, _ := db.GetUserByEmail(ctx, "au-ada@example.test"); hash != "new-hash" {
		t.Errorf("hash = %q", hash)
	}
	if err := db.UpdatePasswordHash(ctx, "au-nobody", "h"); !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("unknown account: %v", err)
	}
	if err := db.UpdatePasswordHash(ctx, "au-ada", ""); err == nil {
		t.Error("an empty hash was accepted")
	}
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

func TestASessionIsFoundByTheHashOfItsTokenTogetherWithItsAccount(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	newUser(t, db, "ada", domain.RoleStaff)
	now := time.Now().UTC().Truncate(time.Millisecond)
	want := newSession(t, db, "au-s1", "au-ada", now)

	s, u, err := db.GetSession(ctx, "th-au-s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != want.ID || s.CSRFToken != "csrf-au-s1" || !s.ExpiresAt.Equal(want.ExpiresAt) || !s.LastSeenAt.Equal(now) {
		t.Errorf("session = %+v, want %+v", s, want)
	}
	if u.ID != "au-ada" || u.Role != domain.RoleStaff {
		t.Errorf("user = %+v", u)
	}
	if _, _, err := db.GetSession(ctx, "th-unknown"); !errors.Is(err, ports.ErrSessionNotFound) {
		t.Errorf("unknown token: %v", err)
	}
}

func TestASessionShowsWhetherItsAccountWasDisabled(t *testing.T) {
	db := authDB(t)
	newUser(t, db, "ada", domain.RoleCustomer)
	newSession(t, db, "au-s1", "au-ada", time.Now())
	if _, err := db.Pool.Exec(context.Background(), "UPDATE users SET disabled_at = now() WHERE id = 'au-ada'"); err != nil {
		t.Fatal(err)
	}
	_, u, err := db.GetSession(context.Background(), "th-au-s1")
	if err != nil || u.DisabledAt == nil {
		t.Errorf("user = %+v, %v, want the disabled time to come through", u, err)
	}
}

func TestASessionNeedsEveryPartAndTheDatabaseKeepsTokensUnique(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	newUser(t, db, "ada", domain.RoleCustomer)
	now := time.Now()
	good := domain.Session{ID: "au-s1", UserID: "au-ada", TokenHash: "th-1", CSRFToken: "c", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	for name, mutate := range map[string]func(s *domain.Session){
		"no id":                func(s *domain.Session) { s.ID = "" },
		"no user":              func(s *domain.Session) { s.UserID = "" },
		"no token hash":        func(s *domain.Session) { s.TokenHash = "" },
		"no csrf token":        func(s *domain.Session) { s.CSRFToken = "" },
		"ends before it began": func(s *domain.Session) { s.ExpiresAt = s.CreatedAt },
	} {
		s := good
		mutate(&s)
		if err := db.CreateSession(ctx, s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := db.CreateSession(ctx, good); err != nil {
		t.Fatal(err)
	}
	dup := good
	dup.ID = "au-s2"
	if err := db.CreateSession(ctx, dup); err == nil {
		t.Error("two sessions with the same token hash were stored")
	}
	ghost := good
	ghost.ID, ghost.TokenHash, ghost.UserID = "au-s3", "th-3", "au-nobody"
	if err := db.CreateSession(ctx, ghost); err == nil {
		t.Error("a session for an account that does not exist was stored")
	}
}

func TestLastSeenOnlyMovesForward(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	newUser(t, db, "ada", domain.RoleCustomer)
	now := time.Now().UTC().Truncate(time.Millisecond)
	newSession(t, db, "au-s1", "au-ada", now)

	if err := db.TouchSession(ctx, "au-s1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchSession(ctx, "au-s1", now.Add(time.Minute)); err != nil { // a slower server writing late
		t.Fatal(err)
	}
	s, _, _ := db.GetSession(ctx, "th-au-s1")
	if !s.LastSeenAt.Equal(now.Add(time.Hour)) {
		t.Errorf("last seen = %v, want it to stay at the later time", s.LastSeenAt)
	}
}

func TestEndingSessions(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	newUser(t, db, "ada", domain.RoleCustomer)
	newUser(t, db, "bob", domain.RoleCustomer)
	now := time.Now()
	for _, id := range []string{"au-a1", "au-a2", "au-a3"} {
		newSession(t, db, id, "au-ada", now)
	}
	newSession(t, db, "au-b1", "au-bob", now)

	if err := db.DeleteSession(ctx, "au-a1"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(ctx, "au-a1"); err != nil {
		t.Errorf("ending a session that is already gone: %v", err)
	}
	n, err := db.DeleteUserSessions(ctx, "au-ada", "au-a2")
	if err != nil || n != 1 {
		t.Errorf("DeleteUserSessions = %d, %v, want 1: a2 is kept and a1 is already gone", n, err)
	}
	if got := sessionIDs(t, db, "au-ada"); len(got) != 1 || got[0] != "au-a2" {
		t.Errorf("ada's sessions = %v, want only au-a2", got)
	}
	if got := sessionIDs(t, db, "au-bob"); len(got) != 1 {
		t.Errorf("bob's sessions = %v: someone else's must not be touched", got)
	}
	if n, _ := db.DeleteUserSessions(ctx, "au-ada", ""); n != 1 {
		t.Errorf("ending all of them ended %d, want 1", n)
	}
}

func TestOldSessionsAreRemovedWhetherTheyEndedOrWentIdle(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	newUser(t, db, "ada", domain.RoleCustomer)
	now := time.Now().UTC()

	newSession(t, db, "au-fresh", "au-ada", now)
	ended := newSession(t, db, "au-ended", "au-ada", now.Add(-20*24*time.Hour))
	_ = ended
	idle := newSession(t, db, "au-idle", "au-ada", now.Add(-10*24*time.Hour))
	_ = idle
	// the idle one is within its lifetime but has not been seen for 3 days
	if _, err := db.Pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $1 WHERE id = 'au-idle'", now.Add(-3*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := db.DeleteExpiredSessions(ctx, now, now.Add(-24*time.Hour))
	if err != nil || n < 2 {
		t.Fatalf("removed %d (%v), want at least the two old ones", n, err)
	}
	if got := sessionIDs(t, db, "au-ada"); len(got) != 1 || got[0] != "au-fresh" {
		t.Errorf("sessions left = %v, want only au-fresh", got)
	}
}

func TestDeletingAnAccountEndsItsSessions(t *testing.T) {
	db := authDB(t)
	newUser(t, db, "ada", domain.RoleCustomer)
	newSession(t, db, "au-s1", "au-ada", time.Now())
	if _, err := db.Pool.Exec(context.Background(), "DELETE FROM users WHERE id = 'au-ada'"); err != nil {
		t.Fatal(err)
	}
	if got := sessionIDs(t, db, "au-ada"); len(got) != 0 {
		t.Errorf("sessions left = %v", got)
	}
}

func TestAuthStoreFailuresAreWrappedAndNeverLookLikeNotFound(t *testing.T) {
	db := testAdapter(t)
	db.Pool.Close() // every query now fails
	ctx := context.Background()
	now := time.Now()
	checks := map[string]error{
		"CreateUser": func() error {
			_, err := db.CreateUser(ctx, domain.User{ID: "au-x", Email: "au-x@e.test", Name: "X", Role: domain.RoleCustomer}, "h")
			return err
		}(),
		"GetUserByEmail": func() error { _, _, err := db.GetUserByEmail(ctx, "au-x@e.test"); return err }(),
		"GetUserByID":    func() error { _, err := db.GetUserByID(ctx, "au-x"); return err }(),
		"UpdatePassword": db.UpdatePasswordHash(ctx, "au-x", "h"),
		"CreateSession":  db.CreateSession(ctx, domain.Session{ID: "au-s", UserID: "au-x", TokenHash: "t", CSRFToken: "c", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}),
		"GetSession":     func() error { _, _, err := db.GetSession(ctx, "t"); return err }(),
		"TouchSession":   db.TouchSession(ctx, "au-s", now),
		"DeleteSession":  db.DeleteSession(ctx, "au-s"),
		"DeleteUserSess": func() error { _, err := db.DeleteUserSessions(ctx, "au-x", ""); return err }(),
		"DeleteExpired":  func() error { _, err := db.DeleteExpiredSessions(ctx, now, now); return err }(),
	}
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s: no error from a closed database", name)
			continue
		}
		if errors.Is(err, ports.ErrUserNotFound) || errors.Is(err, ports.ErrSessionNotFound) || errors.Is(err, ports.ErrEmailTaken) {
			t.Errorf("%s: %v looks like a normal answer, not a failure", name, err)
		}
		if !strings.Contains(err.Error(), "failed to") {
			t.Errorf("%s: %q is not wrapped", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// a customer's own orders
// ---------------------------------------------------------------------------

func orderFor(t *testing.T, db *DBAdapter, id, customer string) {
	t.Helper()
	if _, err := db.CreateOrder(context.Background(), domain.Order{
		ID: id, PaymentType: domain.PaymentCard, CustomerID: customer, RestaurantID: "rest", State: domain.OrderCreated,
		Items: []domain.OrderItem{{SKU: "S", Name: "Meal", Quantity: 1, UnitPriceCents: 500}},
	}); err != nil {
		t.Fatalf("CreateOrder(%s): %v", id, err)
	}
}

func TestACustomerListsOnlyTheirOwnOrdersNewestFirst(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	for _, o := range [][2]string{{"au-o1", "au-ann"}, {"au-o2", "au-bea"}, {"au-o3", "au-ann"}, {"au-o4", "au-ann"}} {
		orderFor(t, db, o[0], o[1])
		time.Sleep(3 * time.Millisecond)
	}
	got, err := db.ListOrdersByCustomer(ctx, "au-ann", 50)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, o := range got {
		if o.Order.CustomerID != "au-ann" {
			t.Errorf("order %s belongs to %s: someone else's order was listed", o.Order.ID, o.Order.CustomerID)
		}
		ids = append(ids, o.Order.ID)
	}
	if strings.Join(ids, ",") != "au-o4,au-o3,au-o1" {
		t.Errorf("orders = %v, want ann's three, newest first", ids)
	}
	if got, _ := db.ListOrdersByCustomer(ctx, "au-nobody", 50); got == nil || len(got) != 0 {
		t.Errorf("a customer with no orders got %v, want an empty list that is not nil", got)
	}
	if got, _ := db.ListOrdersByCustomer(ctx, "au-ann", 2); len(got) != 2 {
		t.Errorf("limit 2 gave %d", len(got))
	}
}

func TestListingByCustomerRefusesNonsenseInsteadOfListingEveryone(t *testing.T) {
	db := authDB(t)
	orderFor(t, db, "au-o1", "au-ann")
	if got, err := db.ListOrdersByCustomer(context.Background(), "", 50); err == nil || got != nil {
		t.Errorf("an empty customer id gave %v, %v: it must be refused, never mean 'everyone'", got, err)
	}
	if _, err := db.ListOrdersByCustomer(context.Background(), "au-ann", 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}
}

func TestCleanupKeepsASessionUntilItsExactEndOrIdleLimit(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	newUser(t, db, "ada", domain.RoleCustomer)
	now := time.Now().UTC().Truncate(time.Microsecond) // what Postgres stores

	// ends exactly now, and was seen just inside the idle limit
	s := domain.Session{ID: "au-ends-now", UserID: "au-ada", TokenHash: "th-ends-now", CSRFToken: "c",
		CreatedAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Minute), ExpiresAt: now}
	if err := db.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	// still live, and last seen exactly at the idle limit
	idle := newSession(t, db, "au-idle-edge", "au-ada", now.Add(-time.Hour))
	if _, err := db.Pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $1 WHERE id = $2", now.Add(-24*time.Hour), idle.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.DeleteExpiredSessions(ctx, now, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := sessionIDs(t, db, "au-ada")
	// a session that ends at this very moment has ended; one seen exactly at the limit has not gone idle yet
	if len(got) != 1 || got[0] != "au-idle-edge" {
		t.Errorf("sessions left = %v, want only au-idle-edge", got)
	}
}
