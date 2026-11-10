package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swiftdrop/internal/catalog/catalogtest"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

var ctx = context.Background()

func kitchen() (*catalogtest.Catalog, Pricer) {
	c := catalogtest.New()
	c.Open("r1", "Corner Kitchen")
	c.Add("burger", "r1", "Smash burger", 950)
	c.Add("fries", "r1", "Fries", 400)
	c.Open("r2", "Noodle Bar")
	c.Add("noodles", "r2", "Pad thai", 1390)
	return c, Pricer{Store: c}
}

func line(id string, qty int) domain.OrderItem { return domain.OrderItem{MenuItemID: id, Quantity: qty} }

func wantProblem(t *testing.T, err error, parts ...string) {
	t.Helper()
	var p *Problem
	if !errors.As(err, &p) {
		t.Fatalf("err = %v, want a *Problem", err)
	}
	for _, part := range parts {
		if !strings.Contains(p.Message, part) {
			t.Errorf("problem %q lacks %q", p.Message, part)
		}
	}
}

// ---- a customer cannot name a price -----------------------------------------------

func TestAMenuLineIsPricedByTheServerWhateverTheRequestSaid(t *testing.T) {
	_, pr := kitchen()
	got, err := pr.Price(ctx, "r1", []domain.OrderItem{
		{MenuItemID: "burger", SKU: "FREE", Name: "Free burger", Quantity: 2, UnitPriceCents: 1},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.OrderItem{MenuItemID: "burger", SKU: "burger", Name: "Smash burger", Quantity: 2, UnitPriceCents: 950}
	if len(got) != 1 || got[0] != want {
		t.Errorf("line = %+v, want %+v: the name, the sku and the price are the menu's", got, want)
	}
	if total := (domain.Order{Items: got}).TotalCents(); total != 1900 {
		t.Errorf("total = %d, want 1900", total)
	}
}

func TestACustomerMayOnlyOrderFromTheMenu(t *testing.T) {
	_, pr := kitchen()
	for name, items := range map[string][]domain.OrderItem{
		"only a custom line":    {{Name: "Burger", Quantity: 1, UnitPriceCents: 1}},
		"a custom line mixed in": {line("burger", 1), {SKU: "X", Name: "Free", Quantity: 1}},
	} {
		got, err := pr.Price(ctx, "r1", items, false)
		wantProblem(t, err, "menuItemId is required")
		if got != nil {
			t.Errorf("%s: lines returned with the problem: %+v", name, got)
		}
	}
	_, err := pr.Price(ctx, "r1", []domain.OrderItem{line("burger", 1), {Name: "x", Quantity: 1}}, false)
	wantProblem(t, err, "items[1]") // names the line
}

func TestTheRestaurantMustBeThereAndOpen(t *testing.T) {
	c, pr := kitchen()
	_, err := pr.Price(ctx, "ghost", []domain.OrderItem{line("burger", 1)}, false)
	wantProblem(t, err, "does not exist")

	closed := false
	if _, err := c.UpdateRestaurant(ctx, "r1", nil, &closed); err != nil {
		t.Fatal(err)
	}
	_, err = pr.Price(ctx, "r1", []domain.OrderItem{line("burger", 1)}, false)
	wantProblem(t, err, "Corner Kitchen is closed")
	// staff may not order from a closed restaurant either, for a menu line
	_, err = pr.Price(ctx, "r1", []domain.OrderItem{line("burger", 1)}, true)
	wantProblem(t, err, "is closed")
}

func TestAnItemMustExistBelongHereAndBeAvailable(t *testing.T) {
	c, pr := kitchen()
	_, err := pr.Price(ctx, "r1", []domain.OrderItem{line("nope", 1)}, false)
	wantProblem(t, err, "items[0]", `"nope"`)

	_, err = pr.Price(ctx, "r1", []domain.OrderItem{line("burger", 1), line("noodles", 1)}, false) // another restaurant's
	wantProblem(t, err, "items[1]", "Pad thai", "not on the menu of Corner Kitchen")

	out := false
	if _, err := c.UpdateMenuItem(ctx, "fries", ports.MenuItemPatch{Available: &out}); err != nil {
		t.Fatal(err)
	}
	_, err = pr.Price(ctx, "r1", []domain.OrderItem{line("fries", 1)}, false)
	wantProblem(t, err, "Fries is not available")
}

func TestQuantitiesAreChecked(t *testing.T) {
	_, pr := kitchen()
	for _, q := range []int{0, -1} {
		_, err := pr.Price(ctx, "r1", []domain.OrderItem{line("burger", q)}, false)
		wantProblem(t, err, "items[0].quantity must be greater than 0")
	}
	if _, err := pr.Price(ctx, "r1", []domain.OrderItem{line("burger", domain.MaxLineQuantity)}, false); err != nil {
		t.Errorf("exactly the maximum: %v", err)
	}
	_, err := pr.Price(ctx, "r1", []domain.OrderItem{line("burger", domain.MaxLineQuantity+1)}, false)
	wantProblem(t, err, "at most 50", "Smash burger")
}

func TestTheSameItemTwiceIsOneLineAndStillWithinTheLimit(t *testing.T) {
	_, pr := kitchen()
	got, err := pr.Price(ctx, "r1", []domain.OrderItem{line("burger", 2), line("fries", 1), line("burger", 3)}, false)
	if err != nil || len(got) != 2 || got[0].MenuItemID != "burger" || got[0].Quantity != 5 || got[1].MenuItemID != "fries" || got[1].Quantity != 1 {
		t.Errorf("lines = %+v, %v, want burger x5 then fries x1", got, err)
	}
	_, err = pr.Price(ctx, "r1", []domain.OrderItem{line("burger", 30), line("burger", 21)}, false)
	wantProblem(t, err, "at most 50")
}

func TestAnOrderHasAtMostThirtyLines(t *testing.T) {
	_, pr := kitchen()
	many := make([]domain.OrderItem, domain.MaxOrderLines+1)
	for i := range many {
		many[i] = line("burger", 1)
	}
	_, err := pr.Price(ctx, "r1", many, false)
	wantProblem(t, err, "at most 30 lines")
	if _, err := pr.Price(ctx, "r1", many[:domain.MaxOrderLines], false); err != nil {
		t.Errorf("exactly the maximum lines: %v", err)
	}
}

// ---- what is left to the validation rules ----------------------------------------

func TestNothingToPriceIsReturnedAsItIs(t *testing.T) {
	c, pr := kitchen()
	c.Err = errors.New("the database is down") // would show if anything were looked up
	for _, restaurant := range []string{"r1", "", "   "} {
		got, err := pr.Price(ctx, restaurant, nil, false)
		if err != nil || got != nil {
			t.Errorf("restaurant %q, no items: %v, %v", restaurant, got, err)
		}
	}
	items := []domain.OrderItem{line("burger", 1)}
	got, err := pr.Price(ctx, "", items, false) // the rule for a missing restaurant says so in its own words
	if err != nil || len(got) != 1 || got[0].MenuItemID != "burger" || got[0].Name != "" {
		t.Errorf("no restaurant: %+v, %v, want the lines untouched", got, err)
	}
}

// ---- staff -------------------------------------------------------------------------

func TestStaffMayEnterCustomLinesWithTheirOwnPrice(t *testing.T) {
	c, pr := kitchen()
	c.Err = errors.New("the database is down") // custom lines need no lookup
	items := []domain.OrderItem{{SKU: "X", Name: "Birthday cake", Quantity: 1, UnitPriceCents: 4200}}
	got, err := pr.Price(ctx, "anywhere", items, true)
	if err != nil || len(got) != 1 || got[0] != items[0] {
		t.Errorf("custom lines = %+v, %v, want them untouched", got, err)
	}
}

func TestStaffMayMixMenuAndCustomLines(t *testing.T) {
	_, pr := kitchen()
	got, err := pr.Price(ctx, "r1", []domain.OrderItem{
		{MenuItemID: "burger", Name: "ignored", UnitPriceCents: 1, Quantity: 1},
		{SKU: "X", Name: "Birthday cake", Quantity: 1, UnitPriceCents: 4200},
	}, true)
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v, %v", got, err)
	}
	if got[0].UnitPriceCents != 950 || got[0].Name != "Smash burger" {
		t.Errorf("the menu line = %+v, want the menu's price even for staff", got[0])
	}
	if got[1].Name != "Birthday cake" || got[1].UnitPriceCents != 4200 || got[1].MenuItemID != "" {
		t.Errorf("the custom line = %+v, want it as typed", got[1])
	}
}

// ---- when storage fails ----------------------------------------------------------

func TestAStorageFailureIsNotAProblemTheCustomerCanFix(t *testing.T) {
	c, pr := kitchen()
	c.Err = errors.New("connection refused")
	_, err := pr.Price(ctx, "r1", []domain.OrderItem{line("burger", 1)}, false)
	var p *Problem
	if err == nil || errors.As(err, &p) {
		t.Errorf("err = %v, want a plain storage error", err)
	}
}

// the restaurant is found but the menu cannot be read
type menuFails struct{ *catalogtest.Catalog }

func (m menuFails) GetMenuItems(context.Context, []string) ([]domain.MenuItem, error) {
	return nil, errors.New("the menu table is locked")
}

func TestAMenuThatCannotBeReadIsAStorageFailureToo(t *testing.T) {
	c, _ := kitchen()
	_, err := Pricer{Store: menuFails{c}}.Price(ctx, "r1", []domain.OrderItem{line("burger", 1)}, false)
	var p *Problem
	if err == nil || errors.As(err, &p) {
		t.Errorf("err = %v, want a plain storage error", err)
	}
}

func TestAProblemReadsAsItsMessage(t *testing.T) {
	if (&Problem{Message: "words"}).Error() != "words" {
		t.Error("Error() is not the message")
	}
}
