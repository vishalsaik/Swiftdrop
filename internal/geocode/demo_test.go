package geocode

import (
	"strings"
	"testing"

	"swiftdrop/internal/domain"
)

func TestTheDemoListFindsStreetsByTheWordsTyped(t *testing.T) {
	got, err := Demo{}.Search(ctx, "indiranagar", 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("indiranagar = %+v, %v, want the 3 streets there", got, err)
	}
	for _, c := range got {
		if !strings.Contains(c.Label, "Indiranagar") || c.City != "Bengaluru" || c.PostalCode == "" || !domain.ValidPoint(c.Latitude, c.Longitude) {
			t.Errorf("candidate = %+v", c)
		}
	}
	// every word must be there, in any order and any capitals
	got, _ = Demo{}.Search(ctx, "ROAD  100 feet", 10)
	if len(got) != 1 || got[0].Line1 != "12 100 Feet Road, Indiranagar" {
		t.Errorf("road 100 feet = %+v", got)
	}
	// the other city
	got, _ = Demo{}.Search(ctx, "bandra", 10)
	if len(got) != 3 || got[0].City != "Mumbai" {
		t.Errorf("bandra = %+v", got)
	}
}

func TestTheDemoListAnswersNothingForWhatItDoesNotKnow(t *testing.T) {
	for _, q := range []string{"zzzz", "indiranagar zzzz", "", "   "} {
		got, err := Demo{}.Search(ctx, q, 10)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("%q = %v, %v, want an empty list", q, got, err)
		}
	}
	if got, _ := (Demo{}).Search(ctx, "indiranagar", 2); len(got) != 2 {
		t.Errorf("limit 2 = %d candidates", len(got))
	}
}

func TestTheDemoListNamesTheNearestStreetWithinTwoKilometres(t *testing.T) {
	// 100 metres from 45 CMH Road, and nearer to it than to any other
	got, err := Demo{}.Reverse(ctx, 12.9725, 77.6420)
	if err != nil || got == nil || got.Line1 != "45 CMH Road, Indiranagar" {
		t.Fatalf("Reverse = %+v, %v, want 45 CMH Road", got, err)
	}
	// the words are the street's, the point is where the person asked about
	if got.Latitude != 12.9725 || got.Longitude != 77.6420 {
		t.Errorf("point = %v, %v, want the one asked about", got.Latitude, got.Longitude)
	}
	// Mumbai's streets name a point in Mumbai
	if got, _ := (Demo{}).Reverse(ctx, 19.0600, 72.8300); got == nil || got.City != "Mumbai" {
		t.Errorf("Mumbai = %+v", got)
	}
	// 4 kilometres from the nearest listed street is too far to name it
	if got, err := (Demo{}).Reverse(ctx, 13.0170, 77.6412); got != nil || err != nil {
		t.Errorf("4 km from Indiranagar = %+v, %v, want nothing", got, err)
	}
	// in the middle of nowhere there is nothing to say
	if got, err := (Demo{}).Reverse(ctx, 64.1466, -21.9426); got != nil || err != nil {
		t.Errorf("Reykjavik = %+v, %v, want nothing", got, err)
	}
}
