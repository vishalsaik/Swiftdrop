// Package drivertest is an in-memory ports.DriverStore for tests. It follows the Postgres adapter's contract
// (an account that is not a driver is not found, a location is kept only while online, going online twice
// keeps the first time), so a test against it is a test of the real behaviour.
package drivertest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

var _ ports.DriverStore = (*Board)(nil)

type entry struct {
	name        string
	disabled    bool
	online      bool
	onlineSince *time.Time
	lat, lng    *float64
	locationAt  *time.Time
	busy        bool
	idleSince   *time.Time
	updatedAt   time.Time
}

// Board keeps drivers in memory. Err makes every call fail, to test what callers do when storage breaks.
type Board struct {
	mu      sync.Mutex
	drivers map[string]*entry
	others  map[string]string // accounts that are not drivers (names only), for DriverNames
	Err     error
	// Now is the clock; a test sets it to make times exact.
	Now func() time.Time
}

// New returns an empty Board.
func New() *Board {
	return &Board{drivers: map[string]*entry{}, others: map[string]string{}, Now: time.Now}
}

// AddDriver makes a driver account (offline, unlocated, free).
func (b *Board) AddDriver(id, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.drivers[id] = &entry{name: name, updatedAt: b.Now()}
}

// AddOther makes an account that is not a driver (a customer or staff member), so a test can check it is
// not found as one.
func (b *Board) AddOther(id, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.others[id] = name
}

// Disable switches a driver's account off; they no longer appear in ListDrivers.
func (b *Board) Disable(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.drivers[id].disabled = true
}

// SetBusy says whether the driver holds an unfinished delivery, which on the real store is read from the
// orders. Finishing one (busy back to false) makes them idle from now.
func (b *Board) SetBusy(id string, busy bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.drivers[id]
	if e.busy && !busy {
		n := b.Now()
		e.idleSince = &n
	}
	e.busy = busy
}

func (b *Board) view(id string, e *entry) domain.Driver {
	d := domain.Driver{
		UserID: id, Name: e.name, Online: e.online, OnlineSince: e.onlineSince,
		Latitude: e.lat, Longitude: e.lng, LocationAt: e.locationAt, Busy: e.busy, UpdatedAt: e.updatedAt,
	}
	if e.online {
		// idle since the later of going online and the last finished delivery
		since := e.onlineSince
		if e.idleSince != nil && (since == nil || e.idleSince.After(*since)) {
			since = e.idleSince
		}
		d.IdleSince = since
	}
	return d
}

func notFound(id string) error { return fmt.Errorf("%w: %s", ports.ErrDriverNotFound, id) }

func (b *Board) GetDriver(_ context.Context, userID string) (domain.Driver, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return domain.Driver{}, b.Err
	}
	e, ok := b.drivers[userID]
	if !ok {
		return domain.Driver{}, notFound(userID)
	}
	return b.view(userID, e), nil
}

func (b *Board) SetOnline(_ context.Context, userID string, online bool) (domain.Driver, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return domain.Driver{}, b.Err
	}
	e, ok := b.drivers[userID]
	if !ok {
		return domain.Driver{}, notFound(userID)
	}
	n := b.Now()
	switch {
	case online && !e.online:
		e.online, e.onlineSince, e.updatedAt = true, &n, n
	case online:
		e.updatedAt = n // already online: the first time is kept
	case e.online:
		e.online, e.onlineSince, e.lat, e.lng, e.locationAt, e.updatedAt = false, nil, nil, nil, nil, n
	}
	return b.view(userID, e), nil
}

func (b *Board) SetLocation(_ context.Context, userID string, lat, lng float64) (domain.Driver, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return domain.Driver{}, b.Err
	}
	e, ok := b.drivers[userID]
	if !ok {
		return domain.Driver{}, notFound(userID)
	}
	if !e.online {
		return domain.Driver{}, fmt.Errorf("%w: %s", ports.ErrDriverOffline, userID)
	}
	n := b.Now()
	e.lat, e.lng, e.locationAt, e.updatedAt = &lat, &lng, &n, n
	return b.view(userID, e), nil
}

func (b *Board) ListDrivers(_ context.Context) ([]domain.Driver, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return nil, b.Err
	}
	out := []domain.Driver{}
	for id, e := range b.drivers {
		if !e.disabled {
			out = append(out, b.view(id, e))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		if a, c := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name); a != c {
			return a < c
		}
		return out[i].UserID < out[j].UserID
	})
	return out, nil
}

func (b *Board) DriverNames(_ context.Context, ids []string) (map[string]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return nil, b.Err
	}
	names := map[string]string{}
	for _, id := range ids {
		if e, ok := b.drivers[id]; ok {
			names[id] = e.name
		} else if n, ok := b.others[id]; ok {
			names[id] = n
		}
	}
	return names, nil
}
