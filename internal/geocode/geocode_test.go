package geocode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// counting is a Geocoder that counts how often it is asked and answers what it is told to.
type counting struct {
	mu       sync.Mutex
	searches int
	reverses int
	found    []Candidate
	one      *Candidate
	err      error
}

func (c *counting) Search(_ context.Context, q string, limit int) ([]Candidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.searches++
	return c.found, c.err
}

func (c *counting) Reverse(_ context.Context, lat, lng float64) (*Candidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reverses++
	return c.one, c.err
}

func cachedWithClock(inner Geocoder, ttl time.Duration, max int) (Geocoder, *time.Time) {
	g := Cached(inner, ttl, max).(*cached)
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	return g, &now
}

func TestAnAnswerIsRememberedWhateverTheCapitalsAndSpaces(t *testing.T) {
	inner := &counting{found: []Candidate{{Line1: "12 MG Road", Latitude: 1, Longitude: 2}}}
	g, _ := cachedWithClock(inner, time.Hour, 10)
	for _, q := range []string{"12 MG Road", "12 mg road", "  12   MG\tRoad  "} {
		got, err := g.Search(ctx, q, 5)
		if err != nil || len(got) != 1 || got[0].Line1 != "12 MG Road" {
			t.Fatalf("%q: %+v, %v", q, got, err)
		}
	}
	if inner.searches != 1 {
		t.Errorf("the service was asked %d times, want once", inner.searches)
	}
	// a different question, or a different number of answers wanted, is a different question
	_, _ = g.Search(ctx, "13 MG Road", 5)
	_, _ = g.Search(ctx, "12 MG Road", 3)
	if inner.searches != 3 {
		t.Errorf("the service was asked %d times, want 3", inner.searches)
	}
}

func TestAnAnswerIsForgottenAfterItsTime(t *testing.T) {
	inner := &counting{found: []Candidate{{Line1: "x"}}}
	g, now := cachedWithClock(inner, time.Hour, 10)
	_, _ = g.Search(ctx, "x", 5)
	*now = now.Add(59 * time.Minute)
	_, _ = g.Search(ctx, "x", 5)
	if inner.searches != 1 {
		t.Errorf("after 59 minutes the service was asked %d times, want 1", inner.searches)
	}
	*now = now.Add(2 * time.Minute) // 61 minutes since it was kept
	_, _ = g.Search(ctx, "x", 5)
	if inner.searches != 2 {
		t.Errorf("after 61 minutes the service was asked %d times, want 2", inner.searches)
	}
	// exactly its time is the end of it
	*now = now.Add(time.Hour)
	_, _ = g.Search(ctx, "x", 5)
	if inner.searches != 3 {
		t.Errorf("after exactly an hour the service was asked %d times, want 3: an answer lives for less than its time", inner.searches)
	}
}

func TestAFailureIsNeverRemembered(t *testing.T) {
	inner := &counting{err: ErrUnavailable}
	g, _ := cachedWithClock(inner, time.Hour, 10)
	for i := 0; i < 3; i++ {
		if _, err := g.Search(ctx, "x", 5); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
		if _, err := g.Reverse(ctx, 1, 2); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("reverse err = %v", err)
		}
	}
	if inner.searches != 3 || inner.reverses != 3 {
		t.Errorf("asked %d and %d times, want every time while it fails", inner.searches, inner.reverses)
	}
	inner.err, inner.found = nil, []Candidate{{Line1: "back"}}
	if got, _ := g.Search(ctx, "x", 5); len(got) != 1 {
		t.Errorf("after the service recovered: %+v", got)
	}
}

func TestNothingFoundIsAnAnswerToo(t *testing.T) {
	inner := &counting{}
	g, _ := cachedWithClock(inner, time.Hour, 10)
	_, _ = g.Search(ctx, "zzz", 5)
	_, _ = g.Search(ctx, "zzz", 5)
	_, _ = g.Reverse(ctx, 1, 2)
	got, _ := g.Reverse(ctx, 1, 2)
	if inner.searches != 1 || inner.reverses != 1 || got != nil {
		t.Errorf("asked %d and %d times, reverse = %v, want each once and nothing", inner.searches, inner.reverses, got)
	}
}

func TestTheTableHoldsOnlyAsManyAnswersAsItWasGiven(t *testing.T) {
	inner := &counting{found: []Candidate{{Line1: "x"}}}
	g, now := cachedWithClock(inner, time.Hour, 3)
	for _, q := range []string{"a", "b", "c"} {
		_, _ = g.Search(ctx, q, 5)
		*now = now.Add(time.Second)
	}
	_, _ = g.Search(ctx, "d", 5) // full: the oldest ("a") goes
	if inner.searches != 4 {
		t.Fatalf("asked %d times", inner.searches)
	}
	_, _ = g.Search(ctx, "b", 5)
	_, _ = g.Search(ctx, "c", 5)
	_, _ = g.Search(ctx, "d", 5)
	if inner.searches != 4 {
		t.Errorf("b, c and d were forgotten: asked %d times", inner.searches)
	}
	_, _ = g.Search(ctx, "a", 5)
	if inner.searches != 5 {
		t.Errorf("a should have been the one forgotten: asked %d times", inner.searches)
	}
	// replacing what is there does not push anything out
	for i := 0; i < 5; i++ {
		*now = now.Add(2 * time.Hour)
		_, _ = g.Search(ctx, "d", 5)
	}
	if n := len(g.(*cached).entries); n > 3 {
		t.Errorf("%d entries, want at most 3", n)
	}
}

func TestAPlaceIsRememberedToAboutElevenMetres(t *testing.T) {
	here := &Candidate{Line1: "45 CMH Road"}
	inner := &counting{one: here}
	g, _ := cachedWithClock(inner, time.Hour, 10)
	_, _ = g.Reverse(ctx, 12.97191, 77.64121)
	_, _ = g.Reverse(ctx, 12.97194, 77.64124) // the same to four decimals
	if inner.reverses != 1 {
		t.Errorf("asked %d times for two points 4 metres apart, want 1", inner.reverses)
	}
	_, _ = g.Reverse(ctx, 12.9721, 77.6412) // 20 metres away
	if inner.reverses != 2 {
		t.Errorf("asked %d times, want a point 20 metres away to be a new question", inner.reverses)
	}
	// negative coordinates round the same way on both sides of zero
	_, _ = g.Reverse(ctx, -33.86881, -151.20931)
	_, _ = g.Reverse(ctx, -33.86884, -151.20934)
	if inner.reverses != 3 {
		t.Errorf("asked %d times, want negative points to be remembered too", inner.reverses)
	}
}

func TestWhatIsKeptCannotBeChangedByWhoeverGotIt(t *testing.T) {
	inner := &counting{found: []Candidate{{Line1: "kept"}}, one: &Candidate{Line1: "kept"}}
	g, _ := cachedWithClock(inner, time.Hour, 10)
	first, _ := g.Search(ctx, "x", 5)
	first[0].Line1 = "changed by the caller"
	again, _ := g.Search(ctx, "x", 5)
	if again[0].Line1 != "kept" {
		t.Errorf("the cache holds %q after a caller changed its copy", again[0].Line1)
	}
	again[0].Line1 = "changed by a second caller, who was served from the cache"
	third, _ := g.Search(ctx, "x", 5)
	if third[0].Line1 != "kept" {
		t.Errorf("the cache holds %q after a caller served from it changed its copy", third[0].Line1)
	}
	one, _ := g.Reverse(ctx, 1, 2)
	one.Line1 = "changed"
	again2, _ := g.Reverse(ctx, 1, 2)
	if again2.Line1 != "kept" {
		t.Errorf("the cache holds %q after a caller changed its copy", again2.Line1)
	}
}

func TestSeveralAskingAtOnceAreSafe(t *testing.T) {
	inner := &counting{found: []Candidate{{Line1: "x"}}, one: &Candidate{Line1: "y"}}
	g := Cached(inner, time.Hour, 5)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = g.Search(ctx, strings.Repeat("a", i%7+1), 5)
			_, _ = g.Reverse(ctx, float64(i%9), 1)
		}(i)
	}
	wg.Wait()
}

// ---- the limiter --------------------------------------------------------------------

func TestAPersonMayAskOnlySoOften(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("ann"); !ok {
			t.Fatalf("use %d was refused", i+1)
		}
		now = now.Add(10 * time.Second)
	}
	// a use is over exactly when its window is: at 60 seconds the first is out, so one more is allowed
	edge := NewLimiter(1, time.Minute)
	t0 := time.Unix(1_700_000_000, 0)
	at := t0
	edge.now = func() time.Time { return at }
	if ok, _ := edge.Allow("x"); !ok {
		t.Fatal("the first use was refused")
	}
	at = t0.Add(59 * time.Second)
	if ok, _ := edge.Allow("x"); ok {
		t.Error("a second use after 59 seconds was allowed")
	}
	at = t0.Add(time.Minute)
	if ok, _ := edge.Allow("x"); !ok {
		t.Error("a second use after exactly a minute was refused: a use is over when its window is")
	}

	ok, wait := l.Allow("ann") // 30 seconds in; the first use was at 0
	if ok || wait != 30*time.Second {
		t.Errorf("the fourth use = %v, wait %v, want refused for 30 seconds", ok, wait)
	}
	if ok, _ := l.Allow("bea"); !ok {
		t.Error("another person was refused because of ann")
	}
	now = now.Add(31 * time.Second) // the first use has aged out
	if ok, _ := l.Allow("ann"); !ok {
		t.Error("ann was refused after her first use aged out")
	}
	// a refused attempt is not a use: asking again at once is refused, and it ends when it should
	if ok, wait := l.Allow("ann"); !ok && wait <= 0 {
		t.Errorf("wait = %v", wait)
	}
}

func TestTheLimiterDoesNotGrowWithEveryoneWhoEverAsked(t *testing.T) {
	l := NewLimiter(1, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 10_001; i++ {
		l.Allow(fmt.Sprintf("user-%d", i))
	}
	now = now.Add(2 * time.Minute) // everyone so far has aged out
	l.Allow("one more")
	if n := len(l.hits); n > 2 {
		t.Errorf("the table holds %d keys after everyone aged out, want it cleaned", n)
	}
}
