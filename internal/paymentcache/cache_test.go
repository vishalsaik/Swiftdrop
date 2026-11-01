package paymentcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// source is a store that counts how often it is asked and answers from a map.
type source struct {
	mu       sync.Mutex
	payments map[string]domain.Payment
	err      error
	reads    int
}

func newSource() *source { return &source{payments: map[string]domain.Payment{}} }

func (s *source) put(orderID string, status domain.PaymentStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[orderID] = domain.Payment{ID: "pay-" + orderID, OrderID: orderID, Status: status}
}

func (s *source) GetPaymentByOrder(_ context.Context, orderID string) (domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.err != nil {
		return domain.Payment{}, s.err
	}
	p, ok := s.payments[orderID]
	if !ok {
		return domain.Payment{}, fmt.Errorf("%w: order %s", ports.ErrPaymentNotFound, orderID)
	}
	return p, nil
}

func (s *source) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func get(t *testing.T, c *Cache, orderID string) domain.Payment {
	t.Helper()
	p, err := c.GetPaymentByOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetPaymentByOrder(%s): %v", orderID, err)
	}
	return p
}

func TestAnAuthorizedPaymentIsReadOnceAndThenKept(t *testing.T) {
	src := newSource()
	src.put("o1", domain.PaymentAuthorized)
	c := New(src, 10)

	for range 5 {
		if p := get(t, c, "o1"); p.ID != "pay-o1" || p.Status != domain.PaymentAuthorized {
			t.Fatalf("payment = %+v", p)
		}
	}
	if src.count() != 1 {
		t.Errorf("the source was asked %d times, want once: an authorized payment never changes", src.count())
	}
	if hits, misses := c.Stats(); hits != 4 || misses != 1 {
		t.Errorf("hits = %d, misses = %d, want 4 and 1", hits, misses)
	}
}

// Everything but an authorized payment can change, so none of it is kept.
func TestPaymentsThatCanStillChangeAreNeverKept(t *testing.T) {
	for _, status := range []domain.PaymentStatus{
		domain.PaymentProcessing, domain.PaymentDeclined, domain.PaymentAbandoned, domain.PaymentNeedsReview,
	} {
		t.Run(string(status), func(t *testing.T) {
			src := newSource()
			src.put("o1", status)
			c := New(src, 10)
			get(t, c, "o1")
			get(t, c, "o1")
			if src.count() != 2 || c.Len() != 0 {
				t.Errorf("reads = %d, held = %d, want every read to go to the source and nothing kept", src.count(), c.Len())
			}
		})
	}
}

func TestACachedOrderSeesTheChangeThatMakesItAuthorized(t *testing.T) {
	src := newSource()
	src.put("o1", domain.PaymentProcessing)
	c := New(src, 10)
	if p := get(t, c, "o1"); p.Status != domain.PaymentProcessing {
		t.Fatalf("payment = %+v", p)
	}
	src.put("o1", domain.PaymentAuthorized) // the answer is recorded
	if p := get(t, c, "o1"); p.Status != domain.PaymentAuthorized {
		t.Errorf("status = %s, want the new answer: a processing payment must not have been kept", p.Status)
	}
	get(t, c, "o1")
	if src.count() != 2 {
		t.Errorf("reads = %d, want 2: kept only once authorized", src.count())
	}
}

// "No payment yet" is the answer to the first /pay of an order; remembering it
// would hide the payment that /pay is about to create.
func TestNoPaymentAndFailuresAreNeverKept(t *testing.T) {
	src := newSource()
	c := New(src, 10)
	for range 2 {
		if _, err := c.GetPaymentByOrder(context.Background(), "o1"); !errors.Is(err, ports.ErrPaymentNotFound) {
			t.Fatalf("error = %v, want not found", err)
		}
	}
	src.put("o1", domain.PaymentAuthorized)
	if p := get(t, c, "o1"); p.Status != domain.PaymentAuthorized {
		t.Errorf("payment = %+v: an earlier 'not found' must not stick", p)
	}

	boom := errors.New("connection refused")
	src.err = boom
	if _, err := c.GetPaymentByOrder(context.Background(), "o2"); !errors.Is(err, boom) {
		t.Errorf("error = %v, want the source's error passed on", err)
	}
	src.err = nil
	src.put("o2", domain.PaymentAuthorized)
	if p := get(t, c, "o2"); p.ID != "pay-o2" {
		t.Errorf("payment = %+v: an earlier failure must not stick", p)
	}
}

func TestTheCacheHoldsAtMostItsLimitAndDropsTheLeastRecentlyUsed(t *testing.T) {
	src := newSource()
	for _, id := range []string{"a", "b", "c"} {
		src.put(id, domain.PaymentAuthorized)
	}
	c := New(src, 2)
	get(t, c, "a")
	get(t, c, "b")
	get(t, c, "a") // a is now more recent than b
	get(t, c, "c") // over the limit: b goes
	if c.Len() != 2 {
		t.Fatalf("held = %d, want 2", c.Len())
	}
	before := src.count()
	get(t, c, "a")
	get(t, c, "c")
	if src.count() != before {
		t.Errorf("a and c should still be held, but the source was asked %d more times", src.count()-before)
	}
	get(t, c, "b")
	if src.count() != before+1 {
		t.Errorf("b was the least recently used and should have been dropped")
	}
}

func TestACacheOfNothingKeepsNothing(t *testing.T) {
	for _, max := range []int{0, -3} {
		src := newSource()
		src.put("o1", domain.PaymentAuthorized)
		c := New(src, max)
		get(t, c, "o1")
		get(t, c, "o1")
		if src.count() != 2 || c.Len() != 0 {
			t.Errorf("max %d: reads = %d, held = %d, want it switched off", max, src.count(), c.Len())
		}
	}
}

func TestStoringAnOrderAgainReplacesItWithoutGrowing(t *testing.T) {
	c := New(newSource(), 5)
	c.store("o1", domain.Payment{ID: "first", Status: domain.PaymentAuthorized})
	c.store("o1", domain.Payment{ID: "second", Status: domain.PaymentAuthorized})
	if c.Len() != 1 {
		t.Errorf("held = %d, want 1", c.Len())
	}
	if p, ok := c.lookup("o1"); !ok || p.ID != "second" {
		t.Errorf("lookup = %+v, %v, want the newer one", p, ok)
	}
}

func TestAReaderCannotChangeWhatIsKept(t *testing.T) {
	src := newSource()
	src.put("o1", domain.PaymentAuthorized)
	c := New(src, 5)
	p := get(t, c, "o1")
	p.Status, p.ID = domain.PaymentDeclined, "tampered"
	if again := get(t, c, "o1"); again.Status != domain.PaymentAuthorized || again.ID != "pay-o1" {
		t.Errorf("payment = %+v, want the original", again)
	}
}

// Many readers of the same and of different orders at once, under -race.
func TestConcurrentReadersAreSafe(t *testing.T) {
	src := newSource()
	for i := range 20 {
		status := domain.PaymentAuthorized
		if i%4 == 0 {
			status = domain.PaymentProcessing
		}
		src.put(fmt.Sprintf("o%d", i), status)
	}
	c := New(src, 8) // smaller than the 15 authorized orders, so it also evicts
	var wg sync.WaitGroup
	for g := range 50 {
		wg.Go(func() {
			for i := range 200 {
				id := fmt.Sprintf("o%d", (g+i)%20)
				p, err := c.GetPaymentByOrder(context.Background(), id)
				if err != nil || p.OrderID != id {
					t.Errorf("GetPaymentByOrder(%s) = %+v, %v", id, p, err)
					return
				}
			}
		})
	}
	wg.Wait()
	if c.Len() > 8 {
		t.Errorf("held = %d, want at most 8", c.Len())
	}
	hits, misses := c.Stats()
	if hits+misses != 50*200 {
		t.Errorf("hits + misses = %d, want %d", hits+misses, 50*200)
	}
}
