package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// An order says where it is going, and its restaurant must deliver there.
// ---------------------------------------------------------------------------

// orderWith is a customer's order of one burger from the restaurant, with extra fields (the address).
func orderWith(id, restaurant, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"id":"` + id + `","restaurantId":"` + restaurant + `","paymentType":"card","items":[{"menuItemId":"burger","quantity":1}]` + extra + `}`
}

func TestAnOrderIsDeliveredToTheAddressItWasPlacedWith(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	wantStatus(t, ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", `"deliveryAddress":`+homeJSON)), http.StatusCreated)
	got := w.db.orders["o1"].DeliveryAddress
	if got == nil || got.Line1 != "12 MG Road" || got.Label != "Home" || got.Landmark != "blue gate" || got.City != "Bengaluru" || got.PostalCode != "560038" || got.Latitude != 12.9719 {
		t.Errorf("stored address = %+v, want the one sent, tidied", got)
	}
}

func TestAnOrderCanUseASavedAddressAndKeepsItsOwnCopy(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	saved := saveAddress(t, ann, homeJSON)
	wantStatus(t, ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", `"deliveryAddressId":"`+saved.ID+`"`)), http.StatusCreated)
	if got := w.db.orders["o1"].DeliveryAddress; got == nil || got.Line1 != "12 MG Road" || got.Landmark != "blue gate" {
		t.Fatalf("stored address = %+v", got)
	}
	// changing, and then deleting, the saved address does not change the order
	wantStatus(t, ann.do(http.MethodPut, "/addresses/"+saved.ID, `{"line1":"99 Somewhere Else","latitude":12.97,"longitude":77.64}`), http.StatusOK)
	wantStatus(t, ann.do(http.MethodDelete, "/addresses/"+saved.ID, ""), http.StatusNoContent)
	if got := w.db.orders["o1"].DeliveryAddress; got == nil || got.Line1 != "12 MG Road" || got.Landmark != "blue gate" {
		t.Errorf("the order's address changed with the saved one: %+v", got)
	}
	// and a deleted address cannot be used again
	rec := ann.do(http.MethodPost, "/orders", orderWith("o2", "r1", `"deliveryAddressId":"`+saved.ID+`"`))
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "no saved address")
}

func TestSomeoneElsesSavedAddressCannotBeUsed(t *testing.T) {
	w := menuWorld(t)
	ann, bea := w.customer(t, "ann@example.com"), w.customer(t, "bea@example.com")
	hers := saveAddress(t, bea, homeJSON)
	rec := ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", `"deliveryAddressId":"`+hers.ID+`"`))
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "no saved address")
	if len(w.db.orders) != 0 {
		t.Error("an order was placed with someone else's address")
	}
}

func TestStaffPlaceAnOrderWithTheCustomersSavedAddressOrOneOfTheirOwn(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	staff := w.staff(t, "dispatch@example.com")
	hers := saveAddress(t, ann, homeJSON)
	mine := saveAddress(t, staff, homeJSON)

	// for ann, with ann's saved address
	wantStatus(t, staff.do(http.MethodPost, "/orders", `{"id":"o1","customerId":"`+ann.userID+`","restaurantId":"r1","paymentType":"card","deliveryAddressId":"`+hers.ID+`","items":[{"menuItemId":"burger","quantity":1}]}`), http.StatusCreated)
	// staff's own saved address is staff's, not ann's
	rec := staff.do(http.MethodPost, "/orders", `{"id":"o2","customerId":"`+ann.userID+`","restaurantId":"r1","paymentType":"card","deliveryAddressId":"`+mine.ID+`","items":[{"menuItemId":"burger","quantity":1}]}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	// a walk-in has no saved addresses: the address is sent with the order
	wantStatus(t, staff.do(http.MethodPost, "/orders", staffOrder("o3", "r1", `[{"menuItemId":"burger","quantity":1}]`)), http.StatusCreated)
	if w.db.orders["o3"].DeliveryAddress == nil || w.db.orders["o3"].DeliveryAddress.Line1 != "1 Test Street" {
		t.Errorf("walk-in order address = %+v", w.db.orders["o3"].DeliveryAddress)
	}
}

func TestAnOrderNeedsExactlyOneAddress(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	saved := saveAddress(t, ann, homeJSON)
	none := ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", ""))
	wantStatus(t, none, http.StatusUnprocessableEntity)
	wantErrorContains(t, none, "delivery address is required", "deliveryAddressId", "deliveryAddress")
	both := ann.do(http.MethodPost, "/orders", orderWith("o2", "r1", `"deliveryAddressId":"`+saved.ID+`","deliveryAddress":`+homeJSON))
	wantStatus(t, both, http.StatusUnprocessableEntity)
	wantErrorContains(t, both, "not both")
	if len(w.db.orders) != 0 {
		t.Error("an order was placed without exactly one address")
	}
}

func TestAnAddressSentWithAnOrderIsCheckedAndTheProblemsAreNamed(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	for name, c := range map[string]struct{ address, want string }{
		"no street":         {`{"latitude":12.9719,"longitude":77.6412}`, "line1 is required"},
		"no place":          {`{"line1":"12 MG Road"}`, "latitude and longitude are required"},
		"a place off earth": {`{"line1":"12 MG Road","latitude":95,"longitude":1}`, "latitude and longitude must be a place on earth"},
		"several things":    {`{"line1":"","city":"` + strings.Repeat("x", 81) + `","latitude":12.9719,"longitude":77.6412}`, "city must be at most 80 characters; line1 is required"},
	} {
		rec := ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", `"deliveryAddress":`+c.address))
		wantStatus(t, rec, http.StatusUnprocessableEntity)
		if !strings.Contains(rec.Body.String(), "deliveryAddress: ") || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %s, want %q", name, rec.Body, c.want)
		}
	}
	wantStatus(t, ann.do(http.MethodPost, "/orders", orderWith("o2", "r1", `"deliveryAddress":{"line1":"x","latitude":1,"longitude":1,"bogus":1}`)), http.StatusBadRequest)
}

func TestARestaurantThatDoesNotDeliverThatFarRefusesTheOrderSayingHowFar(t *testing.T) {
	w := menuWorld(t)
	w.db.Place("r1", "", testLat, testLng, 3) // delivers up to 3 km
	ann := w.customer(t, "ann@example.com")
	// 0.05 degrees of latitude is about 5.6 km north
	far := fmt.Sprintf(`{"line1":"Far Away","latitude":%v,"longitude":%v}`, testLat+0.05, testLng)
	rec := ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", `"deliveryAddress":`+far))
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "Corner Kitchen delivers up to 3.0 km", "5.6 km away")
	if len(w.db.orders) != 0 {
		t.Error("an order was placed beyond the delivery radius")
	}
	// just inside is fine
	near := fmt.Sprintf(`{"line1":"Near","latitude":%v,"longitude":%v}`, testLat+0.025, testLng) // about 2.8 km
	wantStatus(t, ann.do(http.MethodPost, "/orders", orderWith("o2", "r1", `"deliveryAddress":`+near)), http.StatusCreated)
	// a saved address is checked the same way
	saved := saveAddress(t, ann, far)
	wantStatus(t, ann.do(http.MethodPost, "/orders", orderWith("o3", "r1", `"deliveryAddressId":"`+saved.ID+`"`)), http.StatusUnprocessableEntity)
}

func TestARestaurantThatHasNotSaidWhereItIsTakesNoDeliveryOrders(t *testing.T) {
	w := menuWorld(t)
	w.db.Open("nowhere", "Nowhere Cafe")
	w.db.Add("tea", "nowhere", "Tea", 100)
	ann := w.customer(t, "ann@example.com")
	rec := ann.do(http.MethodPost, "/orders", `{"id":"o1","restaurantId":"nowhere","paymentType":"card","deliveryAddress":`+homeJSON+`,"items":[{"menuItemId":"tea","quantity":1}]}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "Nowhere Cafe has not said where it is", "cannot take delivery orders")
	// staff are held to the same rule, even with a custom line
	staff := w.staff(t, "dispatch@example.com")
	rec = staff.do(http.MethodPost, "/orders", `{"id":"o2","customerId":"walk-in","restaurantId":"nowhere","paymentType":"card","deliveryAddress":`+homeJSON+`,"items":[{"sku":"X","name":"Cake","quantity":1,"unitPriceCents":100}]}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "has not said where it is")
}

func TestAnOrderFromARestaurantThatDoesNotExistIsRefusedEvenWithACustomLine(t *testing.T) {
	w := menuWorld(t)
	staff := w.staff(t, "dispatch@example.com")
	rec := staff.do(http.MethodPost, "/orders", `{"id":"o1","customerId":"walk-in","restaurantId":"ghost","paymentType":"card","deliveryAddress":`+homeJSON+`,"items":[{"sku":"X","name":"Cake","quantity":1,"unitPriceCents":100}]}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "does not exist")
}

func TestTheOtherRulesAreSaidBeforeTheAddressIsLookedAt(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	// no items and no address: the items are what is said, as before
	rec := ann.do(http.MethodPost, "/orders", `{"id":"o1","restaurantId":"r1","paymentType":"card","items":[]}`)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	wantErrorContains(t, rec, "items")
	if strings.Contains(rec.Body.String(), "address") {
		t.Errorf("the address was complained about first: %s", rec.Body)
	}
}

func TestAStorageFailureCheckingTheAddressIsAnInternalError(t *testing.T) {
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	saved := saveAddress(t, ann, homeJSON)
	w.db.Book.Err = errors.New("connection refused")
	rec := ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", `"deliveryAddressId":"`+saved.ID+`"`))
	wantStatus(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the cause leaked: %s", rec.Body)
	}
	w.db.Book.Err = nil

	// the restaurant cannot be read, after pricing succeeded: a custom line needs no menu lookup
	staff := w.staff(t, "dispatch@example.com")
	w.db.Catalog.Err = errors.New("connection refused")
	rec = staff.do(http.MethodPost, "/orders", staffOrder("o2", "r1", `[{"sku":"X","name":"Cake","quantity":1,"unitPriceCents":100}]`))
	wantStatus(t, rec, http.StatusInternalServerError)
	if len(w.db.orders) != 0 {
		t.Error("an order was placed that could not be checked")
	}
}

func TestOrdersMadeBeforeAddressesStillReadBack(t *testing.T) {
	w := newWorld(t)
	w.db.orders["old"] = domain.Order{ID: "old", CustomerID: "someone", RestaurantID: "r", State: domain.OrderCreated}
	staff := w.staff(t, "dispatch@example.com")
	rec := staff.do(http.MethodGet, "/orders/old", "")
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "deliveryAddress") {
		t.Errorf("an order with no address shows one: %s", rec.Body)
	}
}
