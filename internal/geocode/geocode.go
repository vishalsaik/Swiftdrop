// Package geocode turns an address someone typed into places on the map, and a place into an
// address. The server asks a Geocoder; which one is behind it (OpenStreetMap's Nominatim, or a small
// built-in list for demos and tests) is decided once at startup. Results are cached, because the
// services behind this are shared and ask for exactly that.
package geocode

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable means the geocoding service could not be asked or did not answer well: it is down,
// slow, refused us, or sent something that is not an answer. It says nothing about the address.
var ErrUnavailable = errors.New("geocoding service unavailable")

// Candidate is a place the service thinks was meant. Latitude and Longitude are where it is; the
// other fields are the words to show and to put on an address.
type Candidate struct {
	// Label is one line to choose by, "12 MG Road, Indiranagar, Bengaluru 560038".
	Label      string  `json:"label"`
	Line1      string  `json:"line1"`
	City       string  `json:"city,omitempty"`
	PostalCode string  `json:"postalCode,omitempty"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
}

// Geocoder finds places by words and words by place.
type Geocoder interface {
	// Search returns up to limit candidates for what was typed, best first; none is not an error.
	Search(ctx context.Context, query string, limit int) ([]Candidate, error)
	// Reverse returns the address at a point, or nil if there is none to name.
	Reverse(ctx context.Context, lat, lng float64) (*Candidate, error)
}

// ---- a cache in front of any Geocoder ----------------------------------------------

type entry struct {
	at         time.Time
	candidates []Candidate
	one        *Candidate
}

type cached struct {
	inner Geocoder
	ttl   time.Duration
	max   int
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]entry
}

// Cached remembers what inner answered, for ttl, and asks it again after that. Only answers are
// kept: a failure is never cached, so the next person tries the service afresh. The table holds at
// most max answers; when it is full the oldest goes. Words are matched without regard to capitals
// and extra spaces, and a place by its coordinates to four decimals (about 11 metres).
func Cached(inner Geocoder, ttl time.Duration, max int) Geocoder {
	return &cached{inner: inner, ttl: ttl, max: max, now: time.Now, entries: map[string]entry{}}
}

func (c *cached) get(key string) (entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok && c.now().Sub(e.at) >= c.ttl {
		delete(c.entries, key)
		return entry{}, false
	}
	return e, ok
}

func (c *cached) put(key string, e entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, have := c.entries[key]; !have && len(c.entries) >= c.max {
		var oldest string
		var oldestAt time.Time
		for k, v := range c.entries {
			if oldest == "" || v.at.Before(oldestAt) {
				oldest, oldestAt = k, v.at
			}
		}
		delete(c.entries, oldest)
	}
	e.at = c.now()
	c.entries[key] = e
}

func (c *cached) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	key := "s|" + strings.Join(strings.Fields(strings.ToLower(query)), " ") + "|" + strconv.Itoa(limit)
	if e, ok := c.get(key); ok {
		return append([]Candidate(nil), e.candidates...), nil
	}
	found, err := c.inner.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	c.put(key, entry{candidates: append([]Candidate(nil), found...)})
	return found, nil
}

func (c *cached) Reverse(ctx context.Context, lat, lng float64) (*Candidate, error) {
	key := "r|" + coordKey(lat) + "|" + coordKey(lng)
	if e, ok := c.get(key); ok {
		return copyOf(e.one), nil
	}
	found, err := c.inner.Reverse(ctx, lat, lng)
	if err != nil {
		return nil, err
	}
	c.put(key, entry{one: copyOf(found)})
	return found, nil
}

func copyOf(c *Candidate) *Candidate {
	if c == nil {
		return nil
	}
	v := *c
	return &v
}

// coordKey is a coordinate to four decimals, as a whole number.
func coordKey(x float64) string { return strconv.FormatInt(int64(math.Round(x*10000)), 10) }

// ---- a small limiter: a person may ask only so often --------------------------------

// Limiter lets each key (a user) use the service at most max times in any window. It is kept in
// memory, so each server counts for itself.
type Limiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
	now    func() time.Time
}

// NewLimiter allows max uses per key in any window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{hits: map[string][]time.Time{}, max: max, window: window, now: time.Now}
}

// Allow records a use and says yes, or says no and how long until the key may use it again.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.hits[key][:0:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.max {
		l.hits[key] = recent
		return false, recent[0].Add(l.window).Sub(now)
	}
	l.hits[key] = append(recent, now)
	// keep the table from growing with every person who ever asked
	if len(l.hits) > 10_000 {
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	return true, 0
}
