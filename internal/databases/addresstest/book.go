// Package addresstest is an in-memory ports.AddressStore for tests. It follows the Postgres
// adapter's contract (oldest first, only the owner finds an address, at most MaxSavedAddresses each),
// so a test against it is a test of the real behaviour.
package addresstest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

var _ ports.AddressStore = (*Book)(nil)

// Book keeps saved addresses in memory. Err makes every call fail, to test what callers do when
// storage breaks.
type Book struct {
	mu    sync.Mutex
	Saved map[string]domain.SavedAddress
	Err   error
	// tick makes creation times distinct and in order, which a real clock does not promise.
	tick int
}

// New returns an empty Book.
func New() *Book { return &Book{Saved: map[string]domain.SavedAddress{}} }

func (b *Book) ListAddresses(_ context.Context, userID string) ([]domain.SavedAddress, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return nil, b.Err
	}
	out := []domain.SavedAddress{}
	for _, a := range b.Saved {
		if a.UserID == userID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (b *Book) GetAddress(_ context.Context, userID, id string) (domain.SavedAddress, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return domain.SavedAddress{}, b.Err
	}
	a, ok := b.Saved[id]
	if !ok || a.UserID != userID {
		return domain.SavedAddress{}, fmt.Errorf("%w: %s", ports.ErrAddressNotFound, id)
	}
	return a, nil
}

func (b *Book) CreateAddress(_ context.Context, a domain.SavedAddress) (domain.SavedAddress, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return domain.SavedAddress{}, b.Err
	}
	have := 0
	for _, x := range b.Saved {
		if x.UserID == a.UserID {
			have++
		}
	}
	if have >= domain.MaxSavedAddresses {
		return domain.SavedAddress{}, ports.ErrTooManyAddresses
	}
	b.tick++
	a.CreatedAt = time.Unix(1_700_000_000, 0).Add(time.Duration(b.tick) * time.Second)
	b.Saved[a.ID] = a
	return a, nil
}

func (b *Book) UpdateAddress(_ context.Context, userID, id string, a domain.Address) (domain.SavedAddress, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return domain.SavedAddress{}, b.Err
	}
	saved, ok := b.Saved[id]
	if !ok || saved.UserID != userID {
		return domain.SavedAddress{}, fmt.Errorf("%w: %s", ports.ErrAddressNotFound, id)
	}
	saved.Address = a
	b.Saved[id] = saved
	return saved, nil
}

func (b *Book) DeleteAddress(_ context.Context, userID, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return b.Err
	}
	a, ok := b.Saved[id]
	if !ok || a.UserID != userID {
		return fmt.Errorf("%w: %s", ports.ErrAddressNotFound, id)
	}
	delete(b.Saved, id)
	return nil
}
