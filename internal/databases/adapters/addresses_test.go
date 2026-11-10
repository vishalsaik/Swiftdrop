package adapters

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Saved addresses and the address an order carries, against the real database. The users they belong
// to are made by newUser (ids starting au-) and removed by authDB, which takes their addresses with them.

func anAddress(id, user string) domain.SavedAddress {
	return domain.SavedAddress{ID: id, UserID: user, Address: domain.Address{
		Label: "Home", Line1: "12 MG Road", Landmark: "blue gate", City: "Bengaluru", PostalCode: "560038", Latitude: 12.9719, Longitude: 77.6412,
	}}
}

func TestAnAddressIsKeptAndReadBack(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann := newUser(t, db, "ad-ann", domain.RoleCustomer)
	made, err := db.CreateAddress(ctx, anAddress("au-addr-1", ann.ID))
	if err != nil {
		t.Fatal(err)
	}
	want := anAddress("au-addr-1", ann.ID)
	if made.Address != want.Address || made.ID != want.ID || made.UserID != ann.ID || made.CreatedAt.IsZero() {
		t.Errorf("created = %+v, want %+v with a creation time", made, want)
	}
	got, err := db.GetAddress(ctx, ann.ID, "au-addr-1")
	if err != nil || got != made {
		t.Errorf("GetAddress = %+v, %v, want what was created", got, err)
	}
	// words and place survive as typed, including characters that are not ASCII and a place at 0, 0
	odd := anAddress("au-addr-2", ann.ID)
	odd.Line1, odd.Landmark, odd.Latitude, odd.Longitude = "ಇಂದಿರಾನಗರ ‘12’ \"quoted\" \\ back", "tab\there", 0, 0
	if made, err = db.CreateAddress(ctx, odd); err != nil || made.Address != odd.Address {
		t.Errorf("an odd address = %+v, %v, want it as given", made.Address, err)
	}
}

func TestAddressesAreListedOldestFirstAndOnlyForTheirOwner(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann, bea := newUser(t, db, "ad-ann", domain.RoleCustomer), newUser(t, db, "ad-bea", domain.RoleCustomer)
	for _, id := range []string{"au-addr-b", "au-addr-a", "au-addr-c"} { // not in id order
		if _, err := db.CreateAddress(ctx, anAddress(id, ann.ID)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateAddress(ctx, anAddress("au-addr-bea", bea.ID)); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListAddresses(ctx, ann.ID)
	if err != nil || len(list) != 3 || list[0].ID != "au-addr-b" || list[1].ID != "au-addr-a" || list[2].ID != "au-addr-c" {
		t.Errorf("ann's list = %+v, %v, want her three in the order saved", list, err)
	}
	if list, _ = db.ListAddresses(ctx, bea.ID); len(list) != 1 || list[0].ID != "au-addr-bea" {
		t.Errorf("bea's list = %+v", list)
	}
	none, err := db.ListAddresses(ctx, "au-nobody")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("a person with none = %v, %v, want an empty list, not nil (it is sent as JSON)", none, err)
	}
}

func TestOnlyTheOwnerFindsChangesOrDeletesAnAddress(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann, bea := newUser(t, db, "ad-ann", domain.RoleCustomer), newUser(t, db, "ad-bea", domain.RoleCustomer)
	if _, err := db.CreateAddress(ctx, anAddress("au-addr-1", ann.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAddress(ctx, bea.ID, "au-addr-1"); !errors.Is(err, ports.ErrAddressNotFound) {
		t.Errorf("bea reading ann's = %v, want ErrAddressNotFound", err)
	}
	if _, err := db.UpdateAddress(ctx, bea.ID, "au-addr-1", domain.Address{Line1: "hijacked", Latitude: 1, Longitude: 1}); !errors.Is(err, ports.ErrAddressNotFound) {
		t.Errorf("bea changing ann's = %v, want ErrAddressNotFound", err)
	}
	if err := db.DeleteAddress(ctx, bea.ID, "au-addr-1"); !errors.Is(err, ports.ErrAddressNotFound) {
		t.Errorf("bea deleting ann's = %v, want ErrAddressNotFound", err)
	}
	if got, err := db.GetAddress(ctx, ann.ID, "au-addr-1"); err != nil || got.Line1 != "12 MG Road" {
		t.Errorf("ann's address after bea's attempts = %+v, %v, want it untouched", got, err)
	}
}

func TestAnAddressCanBeReplacedAndDeleted(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann := newUser(t, db, "ad-ann", domain.RoleCustomer)
	first, _ := db.CreateAddress(ctx, anAddress("au-addr-1", ann.ID))
	got, err := db.UpdateAddress(ctx, ann.ID, "au-addr-1", domain.Address{Line1: "99 New Road", Latitude: 13.5, Longitude: 77.5})
	if err != nil || got.Line1 != "99 New Road" || got.Latitude != 13.5 || got.Label != "" || got.City != "" || got.ID != "au-addr-1" || got.UserID != ann.ID || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("replaced = %+v, %v, want everything replaced and the id, owner and age kept", got, err)
	}
	if _, err := db.UpdateAddress(ctx, ann.ID, "au-nope", domain.Address{Line1: "x"}); !errors.Is(err, ports.ErrAddressNotFound) {
		t.Errorf("updating a missing one = %v", err)
	}
	if err := db.DeleteAddress(ctx, ann.ID, "au-addr-1"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAddress(ctx, ann.ID, "au-addr-1"); !errors.Is(err, ports.ErrAddressNotFound) {
		t.Errorf("deleting twice = %v, want ErrAddressNotFound", err)
	}
	if _, err := db.GetAddress(ctx, ann.ID, "au-addr-1"); !errors.Is(err, ports.ErrAddressNotFound) {
		t.Errorf("reading a deleted one = %v", err)
	}
}

func TestAPersonKeepsAtMostTenAddresses(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann, bea := newUser(t, db, "ad-ann", domain.RoleCustomer), newUser(t, db, "ad-bea", domain.RoleCustomer)
	for i := 0; i < domain.MaxSavedAddresses; i++ {
		if _, err := db.CreateAddress(ctx, anAddress(fmt.Sprintf("au-addr-%02d", i), ann.ID)); err != nil {
			t.Fatalf("address %d: %v", i+1, err)
		}
	}
	if _, err := db.CreateAddress(ctx, anAddress("au-addr-11", ann.ID)); !errors.Is(err, ports.ErrTooManyAddresses) {
		t.Errorf("the eleventh = %v, want ErrTooManyAddresses", err)
	}
	if _, err := db.CreateAddress(ctx, anAddress("au-addr-bea", bea.ID)); err != nil {
		t.Errorf("bea's first: %v (the limit is each person's own)", err)
	}
	if err := db.DeleteAddress(ctx, ann.ID, "au-addr-00"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAddress(ctx, anAddress("au-addr-11", ann.ID)); err != nil {
		t.Errorf("after deleting one: %v", err)
	}
}

func TestTheLimitHoldsWhenManyAreSavedAtTheSameMoment(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann := newUser(t, db, "ad-ann", domain.RoleCustomer)
	var wg sync.WaitGroup
	results := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := db.CreateAddress(ctx, anAddress(fmt.Sprintf("au-addr-race-%02d", i), ann.ID))
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	saved, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			saved++
		case errors.Is(err, ports.ErrTooManyAddresses):
			refused++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if saved != domain.MaxSavedAddresses || refused != 30-domain.MaxSavedAddresses {
		t.Errorf("saved %d and refused %d, want %d and %d", saved, refused, domain.MaxSavedAddresses, 30-domain.MaxSavedAddresses)
	}
	if list, _ := db.ListAddresses(ctx, ann.ID); len(list) != domain.MaxSavedAddresses {
		t.Errorf("%d addresses are kept, want %d", len(list), domain.MaxSavedAddresses)
	}
}

func TestAnAddressThatCannotBeKeptIsRefused(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann := newUser(t, db, "ad-ann", domain.RoleCustomer)
	if _, err := db.CreateAddress(ctx, anAddress("au-addr-1", ann.ID)); err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]domain.SavedAddress{
		"no id":                       anAddress("", ann.ID),
		"no owner":                    anAddress("au-addr-x", ""),
		"no street":                   func() domain.SavedAddress { a := anAddress("au-addr-x", ann.ID); a.Line1 = ""; return a }(),
		"the same id twice":           anAddress("au-addr-1", ann.ID),
		"an owner that is not a user": anAddress("au-addr-x", "au-nobody"),
		"a place off earth":           func() domain.SavedAddress { a := anAddress("au-addr-x", ann.ID); a.Latitude = 95; return a }(),
	} {
		if _, err := db.CreateAddress(ctx, a); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := db.UpdateAddress(ctx, ann.ID, "au-addr-1", domain.Address{Latitude: 1, Longitude: 1}); err == nil {
		t.Error("an update with no street was accepted")
	}
	if _, err := db.UpdateAddress(ctx, ann.ID, "au-addr-1", domain.Address{Line1: "x", Latitude: 1, Longitude: 181}); err == nil {
		t.Error("an update with a longitude off earth was accepted")
	}
	if got, _ := db.GetAddress(ctx, ann.ID, "au-addr-1"); got.Line1 != "12 MG Road" {
		t.Errorf("a refused update changed the address: %+v", got)
	}
	// the table itself refuses what the code would not send
	for name, sql := range map[string]string{
		"an empty street":     `INSERT INTO addresses (id, user_id, line1, latitude, longitude) VALUES ('au-addr-y', '` + ann.ID + `', '', 1, 1)`,
		"a latitude of 91":    `INSERT INTO addresses (id, user_id, line1, latitude, longitude) VALUES ('au-addr-y', '` + ann.ID + `', 'x', 91, 1)`,
		"a longitude of -181": `INSERT INTO addresses (id, user_id, line1, latitude, longitude) VALUES ('au-addr-y', '` + ann.ID + `', 'x', 1, -181)`,
		"no place":            `INSERT INTO addresses (id, user_id, line1) VALUES ('au-addr-y', '` + ann.ID + `', 'x')`,
	} {
		if _, err := db.Pool.Exec(ctx, sql); err == nil {
			t.Errorf("the table accepted %s", name)
		}
	}
}

func TestAPersonsAddressesGoWithThem(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann := newUser(t, db, "ad-ann", domain.RoleCustomer)
	if _, err := db.CreateAddress(ctx, anAddress("au-addr-1", ann.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, ann.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM addresses WHERE user_id = $1`, ann.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d addresses are left for a deleted user (%v)", n, err)
	}
}

// ---- the address an order carries ------------------------------------------------

func TestAnOrderKeepsItsOwnCopyOfTheAddress(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann := newUser(t, db, "ad-ann", domain.RoleCustomer)
	saved, _ := db.CreateAddress(ctx, anAddress("au-addr-1", ann.ID))
	to := saved.Address
	items := []domain.OrderItem{{SKU: "S", Name: "Tea", Quantity: 1, UnitPriceCents: 100}}

	made, err := db.CreateOrder(ctx, domain.Order{ID: "au-o-addr", PaymentType: domain.PaymentCash, CustomerID: ann.ID, RestaurantID: "r", Items: items, State: domain.OrderCreated, DeliveryAddress: &to})
	if err != nil || made.DeliveryAddress == nil || *made.DeliveryAddress != to {
		t.Fatalf("created = %+v, %v, want the address back", made.DeliveryAddress, err)
	}
	// the saved address changes and then goes; the order's copy does not
	if _, err := db.UpdateAddress(ctx, ann.ID, "au-addr-1", domain.Address{Line1: "somewhere else", Latitude: 1, Longitude: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAddress(ctx, ann.ID, "au-addr-1"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetOrderbyId(ctx, "au-o-addr")
	if err != nil || got.DeliveryAddress == nil || *got.DeliveryAddress != to {
		t.Errorf("read back = %+v, %v, want the address the order was placed with", got.DeliveryAddress, err)
	}
	// it is part of every way an order is read
	listed, err := db.ListOrdersByCustomer(ctx, ann.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].Order.DeliveryAddress == nil || listed[0].Order.DeliveryAddress.Landmark != "blue gate" {
		t.Errorf("listed = %+v, %v", listed, err)
	}
	// and it survives the order's own changes
	if updated, err := db.UpdateOrderState(ctx, "au-o-addr", string(domain.OrderCreated), string(domain.OrderCancelled)); err != nil || updated.DeliveryAddress == nil || *updated.DeliveryAddress != to {
		t.Errorf("after a state change = %+v, %v", updated.DeliveryAddress, err)
	}
}

func TestAnOrderWithoutAnAddressReadsBackWithoutOne(t *testing.T) {
	db := authDB(t)
	ctx := context.Background()
	ann := newUser(t, db, "ad-ann", domain.RoleCustomer)
	if _, err := db.CreateOrder(ctx, domain.Order{ID: "au-o-none", PaymentType: domain.PaymentCash, CustomerID: ann.ID, RestaurantID: "r", Items: []domain.OrderItem{{SKU: "S", Name: "Tea", Quantity: 1, UnitPriceCents: 100}}, State: domain.OrderCreated}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetOrderbyId(ctx, "au-o-none")
	if err != nil || got.DeliveryAddress != nil {
		t.Errorf("read back = %+v, %v, want no address (an order from before addresses existed)", got.DeliveryAddress, err)
	}
	var isNull bool
	if err := db.Pool.QueryRow(ctx, `SELECT delivery_address IS NULL FROM orders WHERE id = 'au-o-none'`).Scan(&isNull); err != nil || !isNull {
		t.Errorf("the column holds something for an order with no address (%v)", err)
	}
}

func TestAddressesNeedAConnection(t *testing.T) {
	db := authDB(t)
	db.Pool.Close()
	ctx := context.Background()
	a := anAddress("au-addr-1", "au-x")
	for name, err := range map[string]error{
		"list":   func() error { _, err := db.ListAddresses(ctx, "u"); return err }(),
		"get":    func() error { _, err := db.GetAddress(ctx, "u", "a"); return err }(),
		"create": func() error { _, err := db.CreateAddress(ctx, a); return err }(),
		"update": func() error { _, err := db.UpdateAddress(ctx, "u", "a", a.Address); return err }(),
		"delete": db.DeleteAddress(ctx, "u", "a"),
	} {
		if err == nil || errors.Is(err, ports.ErrAddressNotFound) || errors.Is(err, ports.ErrTooManyAddresses) {
			t.Errorf("%s on a closed pool = %v, want a plain storage error", name, err)
		}
	}
}
