package settle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// spyStore records what Apply asks of the store and answers as told.
type spyStore struct {
	finishErr   error
	completeErr error

	finished  []finishCall
	completed []domain.PaymentStatus
}

type finishCall struct {
	id, from, to string
	status       domain.PaymentStatus
	provider     string
	reason       string
}

func (s *spyStore) FinishPayment(_ context.Context, id string, status domain.PaymentStatus, providerPaymentID, reason, from, to string) (domain.Order, error) {
	s.finished = append(s.finished, finishCall{id, from, to, status, providerPaymentID, reason})
	return domain.Order{}, s.finishErr
}

func (s *spyStore) CompletePayment(_ context.Context, _ string, status domain.PaymentStatus, _, _ string) (domain.Payment, error) {
	s.completed = append(s.completed, status)
	return domain.Payment{}, s.completeErr
}

func payment(attempt int) domain.Payment {
	return domain.Payment{ID: "p1", OrderID: "o1", Attempt: attempt, Status: domain.PaymentProcessing}
}

func TestASuccessPaysTheOrderAndAFailureReopensItUntilTheAttemptsRunOut(t *testing.T) {
	for _, c := range []struct {
		status  domain.PaymentStatus
		attempt int
		want    domain.OrderState
	}{
		{domain.PaymentAuthorized, 1, domain.OrderPaid},
		{domain.PaymentAuthorized, domain.MaxPaymentAttempts, domain.OrderPaid}, // the last attempt can still succeed
		{domain.PaymentDeclined, 1, domain.OrderCreated},
		{domain.PaymentDeclined, domain.MaxPaymentAttempts, domain.OrderCancelled},
		{domain.PaymentAbandoned, 2, domain.OrderCreated},
		{domain.PaymentAbandoned, domain.MaxPaymentAttempts, domain.OrderCancelled},
	} {
		t.Run(fmt.Sprintf("%s on attempt %d", c.status, c.attempt), func(t *testing.T) {
			s := &spyStore{}
			res, err := Apply(context.Background(), s, payment(c.attempt), c.status, "pi_1", "why")
			if err != nil || res != Recorded {
				t.Fatalf("Apply = %v, %v, want Recorded", res, err)
			}
			want := finishCall{"p1", string(domain.OrderPaymentPending), string(c.want), c.status, "pi_1", "why"}
			if len(s.finished) != 1 || s.finished[0] != want {
				t.Errorf("FinishPayment calls = %+v, want exactly %+v", s.finished, want)
			}
			if len(s.completed) != 0 {
				t.Errorf("the answer was also kept separately: %v", s.completed)
			}
		})
	}
}

func TestAnAnswerThatIsAlreadyRecordedChangesNothing(t *testing.T) {
	for _, err := range []error{ports.ErrPaymentNotProcessing, ports.ErrPaymentNotFound} {
		s := &spyStore{finishErr: fmt.Errorf("%w: p1", err)}
		res, got := Apply(context.Background(), s, payment(1), domain.PaymentAuthorized, "pi_1", "")
		if got != nil || res != AlreadyRecorded {
			t.Errorf("%v: Apply = %v, %v, want AlreadyRecorded", err, res, got)
		}
		if len(s.completed) != 0 {
			t.Errorf("%v: something was written: %v", err, s.completed)
		}
	}
}

// The order was cancelled while the payment was in flight: it stays cancelled,
// and the answer is kept on the payment, because it is a fact about money.
func TestAnOrderThatMovedOnKeepsTheAnswerOnThePayment(t *testing.T) {
	s := &spyStore{finishErr: fmt.Errorf("%w: o1", ports.ErrStateConflict)}
	res, err := Apply(context.Background(), s, payment(1), domain.PaymentAuthorized, "pi_1", "")
	if err != nil || res != OrderMovedOn {
		t.Fatalf("Apply = %v, %v, want OrderMovedOn", res, err)
	}
	if len(s.completed) != 1 || s.completed[0] != domain.PaymentAuthorized {
		t.Errorf("kept = %v, want the authorized answer kept without touching the order", s.completed)
	}
}

func TestKeepingTheAnswerOfAnOrderThatMovedOnMayFindItAlreadyKept(t *testing.T) {
	s := &spyStore{finishErr: ports.ErrStateConflict, completeErr: ports.ErrPaymentNotProcessing}
	res, err := Apply(context.Background(), s, payment(1), domain.PaymentAuthorized, "pi_1", "")
	if err != nil || res != OrderMovedOn {
		t.Errorf("Apply = %v, %v, want OrderMovedOn: another writer kept it first, which is fine", res, err)
	}
}

func TestAnAnswerThatCannotBeKeptIsAnUnrecordedError(t *testing.T) {
	conflict := fmt.Errorf("%w: o1", ports.ErrStateConflict)
	down := errors.New("connection refused")
	s := &spyStore{finishErr: conflict, completeErr: down}
	res, err := Apply(context.Background(), s, payment(1), domain.PaymentAuthorized, "pi_1", "")
	var unrecorded *UnrecordedError
	if !errors.As(err, &unrecorded) || res != 0 {
		t.Fatalf("Apply = %v, %v, want an UnrecordedError", res, err)
	}
	if !errors.Is(unrecorded.Conflict, ports.ErrStateConflict) || !errors.Is(unrecorded.Err, down) || !errors.Is(err, down) {
		t.Errorf("error = %+v, want the conflict and the cause kept apart and the cause unwrappable", unrecorded)
	}
	if msg := err.Error(); !strings.Contains(msg, "connection refused") || !strings.Contains(msg, "could not be recorded") {
		t.Errorf("message %q does not say what failed and why", msg)
	}
}

func TestAnyOtherStorageFailureIsReturnedAsItIs(t *testing.T) {
	down := errors.New("connection refused")
	s := &spyStore{finishErr: down}
	res, err := Apply(context.Background(), s, payment(1), domain.PaymentDeclined, "", "no")
	if !errors.Is(err, down) || res != 0 {
		t.Errorf("Apply = %v, %v, want the storage error", res, err)
	}
	var unrecorded *UnrecordedError
	if errors.As(err, &unrecorded) {
		t.Error("a plain storage failure was reported as an unrecorded answer")
	}
	if len(s.completed) != 0 {
		t.Errorf("the answer was kept anyway: %v", s.completed)
	}
}
