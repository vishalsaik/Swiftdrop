package domain

import (
	"strings"
	"testing"
)

func okAddress() Address {
	return Address{Line1: "12 MG Road", Latitude: 12.9719, Longitude: 77.6412}
}

func TestAGoodAddressHasNoProblems(t *testing.T) {
	a := Address{Label: "Home", Line1: "12 MG Road", Landmark: "blue gate", City: "Bengaluru", PostalCode: "560038", Latitude: 12.9719, Longitude: 77.6412}
	if p := a.Problems(); p != nil {
		t.Errorf("problems = %v, want none", p)
	}
	if p := okAddress().Problems(); p != nil {
		t.Errorf("the plainest address has problems: %v", p)
	}
	// 0, 0 is a place
	if p := (Address{Line1: "x"}).Problems(); p != nil {
		t.Errorf("0, 0 has problems: %v", p)
	}
}

func TestTidyTrimsTheWordsAndNothingElse(t *testing.T) {
	got := Address{Label: " Home ", Line1: "\t12 MG Road\n", Landmark: " gate", City: "Bengaluru ", PostalCode: " 560038 ", Latitude: 1.5, Longitude: -2.5}.Tidy()
	want := Address{Label: "Home", Line1: "12 MG Road", Landmark: "gate", City: "Bengaluru", PostalCode: "560038", Latitude: 1.5, Longitude: -2.5}
	if got != want {
		t.Errorf("Tidy = %+v, want %+v", got, want)
	}
}

func TestEachFieldIsCheckedAgainstItsOwnLimit(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*Address)
		field  string
		want   string
	}{
		"no street":           {func(a *Address) { a.Line1 = "" }, "line1", "is required"},
		"a long street":       {func(a *Address) { a.Line1 = strings.Repeat("x", MaxAddressLine+1) }, "line1", "at most 200"},
		"control in a street": {func(a *Address) { a.Line1 = "a\x00b" }, "line1", "control characters"},
		"a long landmark":     {func(a *Address) { a.Landmark = strings.Repeat("x", MaxAddressLine+1) }, "landmark", "at most 200"},
		"control in landmark": {func(a *Address) { a.Landmark = "a\nb" }, "landmark", "control characters"},
		"a long label":        {func(a *Address) { a.Label = strings.Repeat("x", MaxAddressLabel+1) }, "label", "at most 40"},
		"a long city":         {func(a *Address) { a.City = strings.Repeat("x", MaxAddressCity+1) }, "city", "at most 80"},
		"a long postal code":  {func(a *Address) { a.PostalCode = strings.Repeat("1", MaxAddressPostal+1) }, "postalCode", "at most 20"},
		"latitude too big":    {func(a *Address) { a.Latitude = 90.1 }, "latitude", "place on earth"},
		"longitude too small": {func(a *Address) { a.Longitude = -180.1 }, "latitude", "place on earth"},
	} {
		a := okAddress()
		c.change(&a)
		p := a.Problems()
		if !strings.Contains(p[c.field], c.want) {
			t.Errorf("%s: problems = %v, want %q for %s", name, p, c.want, c.field)
		}
		if len(p) != 1 {
			t.Errorf("%s: problems = %v, want only that one", name, p)
		}
	}
}

func TestTheLimitsCountCharactersNotBytes(t *testing.T) {
	a := okAddress()
	a.Line1 = strings.Repeat("é", MaxAddressLine) // 400 bytes
	a.Label = strings.Repeat("é", MaxAddressLabel)
	if p := a.Problems(); p != nil {
		t.Errorf("problems = %v, want the limits to count characters", p)
	}
}

func TestSeveralProblemsAreAllSaid(t *testing.T) {
	p := Address{Line1: "", City: strings.Repeat("x", 81), Latitude: 100}.Problems()
	if len(p) != 3 || p["line1"] == "" || p["city"] == "" || p["latitude"] == "" {
		t.Errorf("problems = %v, want line1, city and latitude", p)
	}
}
