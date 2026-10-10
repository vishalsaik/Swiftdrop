package order

import (
	"errors"
	"strings"
	"testing"

	"swiftdrop/internal/domain"
)

var allStates = []domain.OrderState{
	domain.OrderCreated,
	domain.OrderPaymentPending,
	domain.OrderPaid,
	domain.OrderPreparing,
	domain.OrderPickedUp,
	domain.OrderDelivered,
	domain.OrderCancelled,
}

// legalMoves is the lifecycle written out independently of the production
// table, so a typo in legalTransitions shows up as a failing test instead of
// being copied into the expectation.
var legalMoves = [][2]domain.OrderState{
	{domain.OrderCreated, domain.OrderPaymentPending},
	{domain.OrderCreated, domain.OrderCancelled},
	{domain.OrderPaymentPending, domain.OrderPaid},
	{domain.OrderPaymentPending, domain.OrderCancelled},
	{domain.OrderPaid, domain.OrderPreparing},
	{domain.OrderPaid, domain.OrderCancelled},
	{domain.OrderPreparing, domain.OrderPickedUp},
	{domain.OrderPreparing, domain.OrderCancelled},
	{domain.OrderPickedUp, domain.OrderDelivered},
}

func isLegalMove(from, to domain.OrderState) bool {
	for _, m := range legalMoves {
		if m[0] == from && m[1] == to {
			return true
		}
	}
	return false
}

func TestCanTransitionFullMatrix(t *testing.T) {
	for _, from := range allStates {
		for _, to := range allStates {
			want := isLegalMove(from, to)
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestSelfTransitionsAreIllegal(t *testing.T) {
	for _, s := range allStates {
		if CanTransition(s, s) {
			t.Errorf("CanTransition(%s, %s) = true, want false", s, s)
		}
	}
}

func TestTerminalStatesHaveNoExits(t *testing.T) {
	for _, terminal := range []domain.OrderState{domain.OrderDelivered, domain.OrderCancelled} {
		for _, to := range allStates {
			if CanTransition(terminal, to) {
				t.Errorf("terminal state %s must not move to %s", terminal, to)
			}
		}
	}
}

func TestCancelledOrderCanNeverBeDelivered(t *testing.T) {
	if CanTransition(domain.OrderCancelled, domain.OrderDelivered) {
		t.Fatal("a cancelled order must not become delivered")
	}
}

func TestCancelOnlyBeforePickup(t *testing.T) {
	cancellable := map[domain.OrderState]bool{
		domain.OrderCreated:        true,
		domain.OrderPaymentPending: true,
		domain.OrderPaid:           true,
		domain.OrderPreparing:      true,
	}
	for _, s := range allStates {
		if got := CanTransition(s, domain.OrderCancelled); got != cancellable[s] {
			t.Errorf("CanTransition(%s, cancelled) = %v, want %v", s, got, cancellable[s])
		}
	}
}

func TestNoStateIsSkipped(t *testing.T) {
	skips := [][2]domain.OrderState{
		{domain.OrderCreated, domain.OrderPaid},
		{domain.OrderCreated, domain.OrderPreparing},
		{domain.OrderPaid, domain.OrderPickedUp},
		{domain.OrderPreparing, domain.OrderDelivered},
	}
	for _, m := range skips {
		if CanTransition(m[0], m[1]) {
			t.Errorf("%s -> %s skips a step and must be illegal", m[0], m[1])
		}
	}
}

func TestUnknownStatesAreRejected(t *testing.T) {
	cases := []struct{ from, to domain.OrderState }{
		{"banana", domain.OrderCreated},
		{domain.OrderCreated, "banana"},
		{"", ""},
		{"", domain.OrderCreated},
		{domain.OrderCreated, ""},
	}
	for _, c := range cases {
		if CanTransition(c.from, c.to) {
			t.Errorf("CanTransition(%q, %q) = true, want false", c.from, c.to)
		}
	}
}

func TestEveryStateHasATableEntry(t *testing.T) {
	for _, s := range allStates {
		if _, ok := legalTransitions[s]; !ok {
			t.Errorf("state %s has no entry in legalTransitions; add it (nil if terminal)", s)
		}
	}
	if len(legalTransitions) != len(allStates) {
		t.Errorf("legalTransitions has %d entries, expected %d", len(legalTransitions), len(allStates))
	}
}

func TestEveryStateIsReachableFromCreated(t *testing.T) {
	seen := map[domain.OrderState]bool{domain.OrderCreated: true}
	queue := []domain.OrderState{domain.OrderCreated}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range legalTransitions[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	for _, s := range allStates {
		if !seen[s] {
			t.Errorf("state %s cannot be reached from created", s)
		}
	}
}

func TestHappyPathReachesDelivered(t *testing.T) {
	path := []domain.OrderState{
		domain.OrderCreated,
		domain.OrderPaymentPending,
		domain.OrderPaid,
		domain.OrderPreparing,
		domain.OrderPickedUp,
		domain.OrderDelivered,
	}
	for i := 0; i < len(path)-1; i++ {
		if !CanTransition(path[i], path[i+1]) {
			t.Fatalf("happy path broken at %s -> %s", path[i], path[i+1])
		}
	}
}

func TestApplyLegalReturnsTarget(t *testing.T) {
	got, err := Apply(domain.OrderPaid, domain.OrderPreparing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != domain.OrderPreparing {
		t.Errorf("Apply returned %s, want %s", got, domain.OrderPreparing)
	}
}

func TestApplyIllegalKeepsCurrentAndWrapsSentinel(t *testing.T) {
	got, err := Apply(domain.OrderDelivered, domain.OrderCancelled)
	if err == nil {
		t.Fatal("expected an error for an illegal transition")
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("error %v does not wrap ErrIllegalTransition", err)
	}
	if got != domain.OrderDelivered {
		t.Errorf("Apply returned %s, want the unchanged current state %s", got, domain.OrderDelivered)
	}
	for _, part := range []string{"delivered", "cancelled"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q should mention %q", err.Error(), part)
		}
	}
}

func TestApplyMatchesCanTransition(t *testing.T) {
	for _, from := range allStates {
		for _, to := range allStates {
			got, err := Apply(from, to)
			if CanTransition(from, to) {
				if err != nil || got != to {
					t.Errorf("Apply(%s, %s) = (%s, %v), want (%s, nil)", from, to, got, err, to)
				}
			} else if err == nil || got != from {
				t.Errorf("Apply(%s, %s) = (%s, %v), want (%s, error)", from, to, got, err, from)
			}
		}
	}
}
