package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// Saved addresses: each is its owner's, and only its owner finds it.
// ---------------------------------------------------------------------------

const homeJSON = `{"label":"Home","line1":"12 MG Road","landmark":"blue gate","city":"Bengaluru","postalCode":"560038","latitude":12.9719,"longitude":77.6412}`

type addressesBody struct {
	Addresses []domain.SavedAddress `json:"addresses"`
}

type addressBody struct {
	Address domain.SavedAddress `json:"address"`
}

func listOf(t *testing.T, b *browser) []domain.SavedAddress {
	t.Helper()
	rec := b.do(http.MethodGet, "/addresses", "")
	wantStatus(t, rec, http.StatusOK)
	return decode[addressesBody](t, rec.Body).Addresses
}

func saveAddress(t *testing.T, b *browser, body string) domain.SavedAddress {
	t.Helper()
	rec := b.do(http.MethodPost, "/addresses", body)
	wantStatus(t, rec, http.StatusCreated)
	return decode[addressBody](t, rec.Body).Address
}

func TestSavedAddressesNeedASignInAndTheCSRFToken(t *testing.T) {
	w := newWorld(t)
	anon := w.browser(t)
	for _, c := range [][2]string{{http.MethodGet, "/addresses"}, {http.MethodPost, "/addresses"}, {http.MethodPut, "/addresses/x"}, {http.MethodDelete, "/addresses/x"}} {
		wantStatus(t, anon.do(c[0], c[1], `{}`), http.StatusUnauthorized)
	}
	ann := w.customer(t, "ann@example.com")
	ann.noCSRF = true
	rec := ann.do(http.MethodPost, "/addresses", homeJSON)
	wantStatus(t, rec, http.StatusForbidden)
	wantErrorContains(t, rec, "CSRF")
	if len(w.db.Saved) != 0 {
		t.Error("an address was saved without the token")
	}
}

func TestAPersonSavesAnAddressAndSeesItAgain(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	if got := listOf(t, ann); len(got) != 0 {
		t.Fatalf("a new customer has %v", got)
	}
	saved := saveAddress(t, ann, `{"label":"  Home ","line1":"  12 MG Road ","landmark":" blue gate","city":"Bengaluru","postalCode":" 560038 ","latitude":12.9719,"longitude":77.6412}`)
	if saved.ID == "" || saved.UserID != ann.userID || saved.Label != "Home" || saved.Line1 != "12 MG Road" || saved.Landmark != "blue gate" || saved.PostalCode != "560038" ||
		saved.Latitude != 12.9719 || saved.Longitude != 77.6412 || saved.CreatedAt.IsZero() {
		t.Errorf("saved = %+v, want it trimmed, owned by ann, placed", saved)
	}
	rec := ann.do(http.MethodGet, "/addresses", "")
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := decode[addressesBody](t, rec.Body).Addresses; len(got) != 1 || got[0].ID != saved.ID {
		t.Errorf("list = %+v", got)
	}
	// the answer says who it belongs to and nothing about anyone else
	if !strings.Contains(rec.Body.String(), `"userId":"`+ann.userID+`"`) {
		t.Errorf("the owner is not in the answer: %s", rec.Body)
	}
}

func TestStaffKeepTheirOwnAddressesToo(t *testing.T) {
	w := newWorld(t)
	staff := w.staff(t, "dispatch@example.com")
	saved := saveAddress(t, staff, homeJSON)
	if got := listOf(t, staff); len(got) != 1 || got[0].ID != saved.ID {
		t.Errorf("staff list = %+v", got)
	}
}

func TestAddressesAreListedOldestFirstAndEachPersonSeesOnlyTheirOwn(t *testing.T) {
	w := newWorld(t)
	ann, bea := w.customer(t, "ann@example.com"), w.customer(t, "bea@example.com")
	first := saveAddress(t, ann, `{"label":"Home","line1":"1 First Street","latitude":1,"longitude":1}`)
	second := saveAddress(t, ann, `{"label":"Work","line1":"2 Second Street","latitude":2,"longitude":2}`)
	hers := saveAddress(t, bea, `{"label":"Bea","line1":"3 Third Street","latitude":3,"longitude":3}`)
	if got := listOf(t, ann); len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
		t.Errorf("ann's list = %+v, want her two, oldest first", got)
	}
	if got := listOf(t, bea); len(got) != 1 || got[0].ID != hers.ID {
		t.Errorf("bea's list = %+v", got)
	}
}

func TestSomeoneElsesAddressIsAsGoodAsMissing(t *testing.T) {
	w := newWorld(t)
	ann, bea := w.customer(t, "ann@example.com"), w.customer(t, "bea@example.com")
	hers := saveAddress(t, bea, homeJSON)
	for _, id := range []string{hers.ID, "no-such-address"} {
		wantStatus(t, ann.do(http.MethodPut, "/addresses/"+id, homeJSON), http.StatusNotFound)
		rec := ann.do(http.MethodDelete, "/addresses/"+id, "")
		wantStatus(t, rec, http.StatusNotFound)
		wantErrorContains(t, rec, "no such saved address")
	}
	if got := listOf(t, bea); len(got) != 1 || got[0].Line1 != "12 MG Road" {
		t.Errorf("bea's address was changed or removed by ann: %+v", got)
	}
}

func TestAnAddressCanBeReplacedAndDeleted(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	saved := saveAddress(t, ann, homeJSON)
	rec := ann.do(http.MethodPut, "/addresses/"+saved.ID, `{"line1":" 99 New Road ","latitude":13.5,"longitude":77.5}`)
	wantStatus(t, rec, http.StatusOK)
	got := decode[addressBody](t, rec.Body).Address
	if got.ID != saved.ID || got.Line1 != "99 New Road" || got.Latitude != 13.5 || got.Label != "" || got.City != "" || got.UserID != ann.userID {
		t.Errorf("replaced = %+v, want it all replaced (what is left out is cleared), the id and owner kept", got)
	}
	wantStatus(t, ann.do(http.MethodDelete, "/addresses/"+saved.ID, ""), http.StatusNoContent)
	if got := listOf(t, ann); len(got) != 0 {
		t.Errorf("after deleting = %+v", got)
	}
	wantStatus(t, ann.do(http.MethodDelete, "/addresses/"+saved.ID, ""), http.StatusNotFound)
}

func TestAnAddressNamesWhatIsWrongWithIt(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	long := strings.Repeat("x", 201)
	for name, c := range map[string]struct{ body, field string }{
		"no street":           {`{"latitude":1,"longitude":1}`, "line1"},
		"a blank street":      {`{"line1":"   ","latitude":1,"longitude":1}`, "line1"},
		"a long street":       {`{"line1":"` + long + `","latitude":1,"longitude":1}`, "line1"},
		"control in a street": {`{"line1":"a\u0007b","latitude":1,"longitude":1}`, "line1"},
		"a long landmark":     {`{"line1":"x","landmark":"` + long + `","latitude":1,"longitude":1}`, "landmark"},
		"a long label":        {`{"line1":"x","label":"` + strings.Repeat("x", 41) + `","latitude":1,"longitude":1}`, "label"},
		"a long city":         {`{"line1":"x","city":"` + strings.Repeat("x", 81) + `","latitude":1,"longitude":1}`, "city"},
		"a long postal code":  {`{"line1":"x","postalCode":"` + strings.Repeat("1", 21) + `","latitude":1,"longitude":1}`, "postalCode"},
		"no place at all":     {`{"line1":"x"}`, "latitude"},
		"latitude alone":      {`{"line1":"x","latitude":1}`, "latitude"},
		"longitude alone":     {`{"line1":"x","longitude":1}`, "latitude"},
		"latitude too big":    {`{"line1":"x","latitude":91,"longitude":1}`, "latitude"},
		"longitude too big":   {`{"line1":"x","latitude":1,"longitude":-181}`, "latitude"},
	} {
		for _, call := range [][2]string{{http.MethodPost, "/addresses"}, {http.MethodPut, "/addresses/whatever"}} {
			rec := ann.do(call[0], call[1], c.body)
			if call[0] == http.MethodPut && rec.Code == http.StatusNotFound {
				t.Errorf("%s: the address was looked up before it was checked", name)
				continue
			}
			wantStatus(t, rec, http.StatusUnprocessableEntity)
			if decode[fieldsBody](t, rec.Body).Fields[c.field] == "" {
				t.Errorf("%s: %q not named: %s", name, c.field, rec.Body)
			}
		}
	}
	// 0, 0 is a place (in the sea) and is not the same as leaving the place out
	saveAddress(t, ann, `{"line1":"x","latitude":0,"longitude":0}`)
	// the edges are fine
	saveAddress(t, ann, `{"line1":"`+strings.Repeat("é", 200)+`","landmark":"`+strings.Repeat("é", 200)+`","label":"`+strings.Repeat("é", 40)+`","latitude":90,"longitude":-180}`)
	wantStatus(t, ann.do(http.MethodPost, "/addresses", `{"line1":"x","latitude":1,"longitude":1,"colour":"red"}`), http.StatusBadRequest)
	wantStatus(t, ann.do(http.MethodPut, "/addresses/whatever", `{"line1":"x","latitude":1,"longitude":1,"colour":"red"}`), http.StatusBadRequest)
	if got := listOf(t, ann); len(got) != 2 {
		t.Errorf("%d addresses, want only the two good ones", len(got))
	}
}

func TestAPersonMayKeepTenAddresses(t *testing.T) {
	w := newWorld(t)
	ann, bea := w.customer(t, "ann@example.com"), w.customer(t, "bea@example.com")
	for i := 0; i < domain.MaxSavedAddresses; i++ {
		saveAddress(t, ann, fmt.Sprintf(`{"line1":"%d Street","latitude":1,"longitude":1}`, i))
	}
	rec := ann.do(http.MethodPost, "/addresses", homeJSON)
	wantStatus(t, rec, http.StatusConflict)
	wantErrorContains(t, rec, "at most 10", "delete one")
	saveAddress(t, bea, homeJSON) // the limit is each person's own
	// deleting one makes room
	first := listOf(t, ann)[0]
	wantStatus(t, ann.do(http.MethodDelete, "/addresses/"+first.ID, ""), http.StatusNoContent)
	saveAddress(t, ann, homeJSON)
}

func TestAStorageFailureWithAddressesIsAnInternalError(t *testing.T) {
	w := newWorld(t)
	ann := w.customer(t, "ann@example.com")
	saved := saveAddress(t, ann, homeJSON)
	w.db.Book.Err = errors.New("connection refused")
	for _, c := range [][3]string{
		{http.MethodGet, "/addresses", ""},
		{http.MethodPost, "/addresses", homeJSON},
		{http.MethodPut, "/addresses/" + saved.ID, homeJSON},
		{http.MethodDelete, "/addresses/" + saved.ID, ""},
	} {
		rec := ann.do(c[0], c[1], c[2])
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "connection refused") {
			t.Errorf("%s leaked the cause: %s", c[1], rec.Body)
		}
	}
}

func TestTheOrderViewsCarryTheAddress(t *testing.T) {
	// the address an order was placed with is part of how it is read back
	w := menuWorld(t)
	ann := w.customer(t, "ann@example.com")
	wantStatus(t, ann.do(http.MethodPost, "/orders", orderWith("o1", "r1", `"deliveryAddress":`+homeJSON)), http.StatusCreated)
	var out struct {
		DeliveryAddress *domain.Address `json:"deliveryAddress"`
	}
	rec := ann.do(http.MethodGet, "/orders/o1", "")
	wantStatus(t, rec, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.DeliveryAddress == nil || out.DeliveryAddress.Line1 != "12 MG Road" || out.DeliveryAddress.Landmark != "blue gate" {
		t.Errorf("the order view = %s, want the address", rec.Body)
	}
}
