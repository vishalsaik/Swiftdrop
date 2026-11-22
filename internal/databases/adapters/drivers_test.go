package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Drivers, against the real database. Accounts made here are the au- accounts of authDB, and a driver's row
// goes with its account.

func driverUser(t *testing.T, db *DBAdapter, name string) domain.User {
	t.Helper()
	return newUser(t, db, name, domain.RoleDriver)
}

// assignTo gives an existing order (made by newRow) to a driver and puts it in a state.
func assignTo(t *testing.T, db *DBAdapter, orderID, driverID string, state domain.OrderState) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		`UPDATE orders SET assigned_driver_id = $2, state = $3, updated_at = now() WHERE id = $1`, orderID, driverID, string(state)); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyDriverAccountsAreDrivers(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "drv-staff")
	customer := newUser(t, db, "drv-cust", domain.RoleCustomer)

	for name, id := range map[string]string{"staff": staff.ID, "customer": customer.ID, "nobody": "au-nobody"} {
		if _, err := db.GetDriver(ctx, id); !errors.Is(err, ports.ErrDriverNotFound) {
			t.Errorf("%s: GetDriver error = %v, want ErrDriverNotFound", name, err)
		}
		if _, err := db.SetOnline(ctx, id, true); !errors.Is(err, ports.ErrDriverNotFound) {
			t.Errorf("%s: SetOnline(true) error = %v, want ErrDriverNotFound", name, err)
		}
		if _, err := db.SetOnline(ctx, id, false); !errors.Is(err, ports.ErrDriverNotFound) {
			t.Errorf("%s: SetOnline(false) error = %v, want ErrDriverNotFound", name, err)
		}
		if _, err := db.SetLocation(ctx, id, 12.9, 77.6); !errors.Is(err, ports.ErrDriverNotFound) {
			t.Errorf("%s: SetLocation error = %v, want ErrDriverNotFound", name, err)
		}
	}
	var rows int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM drivers WHERE user_id LIKE 'au-%'`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("a non-driver got %d rows in drivers (err %v), want none", rows, err)
	}
}

func TestADriverWhoHasNeverBeenOnlineReadsAsOffline(t *testing.T) {
	db := authDB(t)
	u := driverUser(t, db, "drv-new")
	d, err := db.GetDriver(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.UserID != u.ID || d.Name != u.Name {
		t.Errorf("driver = %+v, want the account's id and name", d)
	}
	if d.Online || d.Busy || d.Located() || d.OnlineSince != nil || d.IdleSince != nil {
		t.Errorf("a new driver should be offline, free, unlocated and not idle yet: %+v", d)
	}
	if d.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestGoingOnlineTwiceKeepsTheFirstTime(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	u := driverUser(t, db, "drv-twice")

	first, err := db.SetOnline(ctx, u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Online || first.OnlineSince == nil || first.IdleSince == nil {
		t.Fatalf("going online should set online, the time and the idle clock: %+v", first)
	}
	time.Sleep(20 * time.Millisecond)
	second, err := db.SetOnline(ctx, u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !second.OnlineSince.Equal(*first.OnlineSince) || !second.IdleSince.Equal(*first.IdleSince) {
		t.Errorf("going online again moved the clock: %v then %v", first.OnlineSince, second.OnlineSince)
	}

	off, err := db.SetOnline(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if off.Online || off.OnlineSince != nil || off.IdleSince != nil {
		t.Errorf("going offline should clear the clock: %+v", off)
	}
	again, err := db.SetOnline(ctx, u.ID, false)
	if err != nil || again.Online {
		t.Errorf("saying offline to an offline driver = %+v, %v; want offline and no error", again, err)
	}
	back, err := db.SetOnline(ctx, u.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !back.OnlineSince.After(*first.OnlineSince) {
		t.Errorf("a new session online should start a new clock: %v is not after %v", back.OnlineSince, first.OnlineSince)
	}
}

func TestALocationIsKeptOnlyWhileOnlineAndNeverAsATrail(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	u := driverUser(t, db, "drv-loc")

	// before ever being online, and while offline: refused, and nothing is written
	if _, err := db.SetLocation(ctx, u.ID, 12.9, 77.6); !errors.Is(err, ports.ErrDriverOffline) {
		t.Fatalf("a driver who was never online: error = %v, want ErrDriverOffline", err)
	}
	if _, err := db.SetOnline(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	d, err := db.SetLocation(ctx, u.ID, 12.9719, 77.6412)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Located() || *d.Latitude != 12.9719 || *d.Longitude != 77.6412 || d.LocationAt == nil {
		t.Fatalf("the point was not kept: %+v", d)
	}
	d, err = db.SetLocation(ctx, u.ID, 13.0, 77.7)
	if err != nil {
		t.Fatal(err)
	}
	if *d.Latitude != 13.0 || *d.Longitude != 77.7 {
		t.Errorf("the next point should replace the last: %+v", d)
	}
	var rows int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM drivers WHERE user_id = $1`, u.ID).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("a driver has %d rows (err %v), want 1: a point replaces the last, never a trail", rows, err)
	}

	off, err := db.SetOnline(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if off.Located() || off.LocationAt != nil {
		t.Errorf("going offline should forget the point: %+v", off)
	}
	if _, err := db.SetLocation(ctx, u.ID, 12.9, 77.6); !errors.Is(err, ports.ErrDriverOffline) {
		t.Errorf("a location from an offline driver: error = %v, want ErrDriverOffline", err)
	}
	if got, _ := db.GetDriver(ctx, u.ID); got.Located() {
		t.Error("a refused location was kept anyway")
	}
}

func TestTheDatabaseRefusesAHalfPointAndAnUnknownRole(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	u := driverUser(t, db, "drv-check")
	if _, err := db.SetOnline(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE drivers SET latitude = 12.9, longitude = NULL WHERE user_id = $1`, u.ID); err == nil {
		t.Error("a latitude without a longitude was accepted")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE drivers SET latitude = 91, longitude = 10 WHERE user_id = $1`, u.ID); err == nil {
		t.Error("a latitude of 91 was accepted")
	}
	// the role check was re-created for drivers and still refuses anything else
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO users (id, email, name, role, password_hash) VALUES ('au-bogus', 'au-bogus@example.test', 'Bogus', 'courier', 'h')`); err == nil {
		t.Error("a role that is not customer, staff or driver was accepted")
	}
}

func TestADriverIsBusyWhileTheyHoldAnUnfinishedOrderAndIdleAfterwards(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	u := driverUser(t, db, "drv-busy")
	if _, err := db.SetOnline(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetDriver(ctx, u.ID)

	newRow(t, db, "it-drv-busy", domain.OrderPaid)
	time.Sleep(20 * time.Millisecond)

	for _, state := range []domain.OrderState{domain.OrderPreparing, domain.OrderPickedUp} {
		assignTo(t, db, "it-drv-busy", u.ID, state)
		d, err := db.GetDriver(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Busy {
			t.Errorf("a driver holding a %s order is not busy", state)
		}
	}

	// delivered: free again, and idle from the moment the order last changed
	assignTo(t, db, "it-drv-busy", u.ID, domain.OrderDelivered)
	d, _ := db.GetDriver(ctx, u.ID)
	if d.Busy {
		t.Error("a driver whose order was delivered is still busy")
	}
	if d.IdleSince == nil || !d.IdleSince.After(*before.IdleSince) {
		t.Errorf("idle since = %v, want after going online (%v): finishing a job restarts the idle clock", d.IdleSince, before.IdleSince)
	}

	// cancelled while with the driver: free as well
	assignTo(t, db, "it-drv-busy", u.ID, domain.OrderCancelled)
	if d, _ := db.GetDriver(ctx, u.ID); d.Busy {
		t.Error("a driver whose order was cancelled is still busy")
	}

	// an order that is not theirs makes no difference
	other := driverUser(t, db, "drv-busy-other")
	if d, _ := db.GetDriver(ctx, other.ID); d.Busy {
		t.Error("another driver is busy because of this order")
	}
}

func TestListDriversPutsOnlineFirstThenByNameAndSkipsDisabledAccounts(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	staff := staffUser(t, db, "lst-staff")
	zed := driverUser(t, db, "lst-zed")
	amy := driverUser(t, db, "lst-amy")
	bob := driverUser(t, db, "lst-bob")
	gone := driverUser(t, db, "lst-gone")
	newUser(t, db, "lst-customer", domain.RoleCustomer)
	for _, id := range []string{zed.ID, bob.ID, gone.ID} {
		if _, err := db.SetOnline(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := db.SetUserDisabled(ctx, actorOf(staff), gone.ID, true); err != nil {
		t.Fatal(err)
	}

	all, err := db.ListDrivers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ours []string
	for _, d := range all {
		if len(d.UserID) > 3 && d.UserID[:3] == "au-" {
			ours = append(ours, d.UserID)
		}
	}
	want := []string{bob.ID, zed.ID, amy.ID} // online by name, then offline
	if len(ours) != len(want) {
		t.Fatalf("drivers = %v, want %v (no customers, staff or disabled accounts)", ours, want)
	}
	for i := range want {
		if ours[i] != want[i] {
			t.Fatalf("drivers = %v, want %v", ours, want)
		}
	}
}

func TestDriverNames(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	a := driverUser(t, db, "nm-a")
	b := staffUser(t, db, "nm-b")

	names, err := db.DriverNames(ctx, []string{a.ID, b.ID, "au-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[a.ID] != a.Name || names[b.ID] != b.Name {
		t.Errorf("names = %v, want the two accounts that exist", names)
	}
	none, err := db.DriverNames(ctx, nil)
	if err != nil || len(none) != 0 {
		t.Errorf("no ids = %v, %v; want an empty answer", none, err)
	}
}
