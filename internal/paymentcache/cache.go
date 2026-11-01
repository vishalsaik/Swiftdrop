// Package paymentcache keeps the payments that can never change in memory, so a
// retry of /pay on an order that is already paid does not ask the database.
//
// What may be cached is decided by what cannot go stale. An authorized payment is
// final: it never becomes anything else, and while it exists the order can have no
// other live payment (the database allows one), so "the payment of this order" is
// the same row for ever. Everything else changes: a processing payment is
// answered, a declined one is followed by a new attempt, one under review is
// settled. Those are never kept, and neither are errors or "not found" (the first
// /pay of an order must see that there is no payment yet).
//
// The cache holds the payment as it was read, refund fields included, and those DO
// change (a refund is made later). So it is only for the caller that needs the
// payment's status, and a screen that shows the refund must read the database.
package paymentcache

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"

	"swiftdrop/internal/domain"
)

// Source is where a payment is read from when the cache does not have it.
type Source interface {
	GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error)
}

// Cache is a bounded, least-recently-used cache of authorized payments by order id.
// It is safe for concurrent use.
type Cache struct {
	src Source
	max int

	mu      sync.Mutex
	entries map[string]*list.Element // order id -> element holding an entry
	recent  *list.List               // front is the most recently used

	hits, misses atomic.Int64
}

type entry struct {
	orderID string
	payment domain.Payment
}

// New returns a Cache in front of src that keeps at most max payments. A max of
// zero or less keeps nothing: every read goes to src.
func New(src Source, max int) *Cache {
	return &Cache{src: src, max: max, entries: map[string]*list.Element{}, recent: list.New()}
}

// GetPaymentByOrder returns the order's payment, from memory when it is an
// authorized one the cache has seen, otherwise from the source.
func (c *Cache) GetPaymentByOrder(ctx context.Context, orderID string) (domain.Payment, error) {
	if p, ok := c.lookup(orderID); ok {
		c.hits.Add(1)
		return p, nil
	}
	c.misses.Add(1)
	p, err := c.src.GetPaymentByOrder(ctx, orderID)
	if err == nil && p.Status == domain.PaymentAuthorized {
		c.store(orderID, p)
	}
	return p, err
}

// Stats says how many reads were answered from memory and how many went to the source.
func (c *Cache) Stats() (hits, misses int64) { return c.hits.Load(), c.misses.Load() }

// Len is how many payments are held.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache) lookup(orderID string) (domain.Payment, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[orderID]
	if !ok {
		return domain.Payment{}, false
	}
	c.recent.MoveToFront(el)
	return el.Value.(*entry).payment, true
}

func (c *Cache) store(orderID string, p domain.Payment) {
	if c.max <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[orderID]; ok {
		el.Value.(*entry).payment = p
		c.recent.MoveToFront(el)
		return
	}
	c.entries[orderID] = c.recent.PushFront(&entry{orderID: orderID, payment: p})
	for len(c.entries) > c.max {
		oldest := c.recent.Back()
		c.recent.Remove(oldest)
		delete(c.entries, oldest.Value.(*entry).orderID)
	}
}
