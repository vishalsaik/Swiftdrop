package auth

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func TestAKeyIsBlockedOnceItHasUsedItsHitsAndHowLongIsSaid(t *testing.T) {
	l := newLimiter(3, 10*time.Minute)
	for i := range 3 {
		if _, blocked := l.blocked("k", t0.Add(time.Duration(i)*time.Minute)); blocked {
			t.Fatalf("blocked after only %d hits", i)
		}
		l.hit("k", t0.Add(time.Duration(i)*time.Minute))
	}
	wait, blocked := l.blocked("k", t0.Add(3*time.Minute))
	if !blocked || wait != 7*time.Minute {
		t.Errorf("blocked=%v wait=%v, want blocked for 7m: the first hit leaves the window at 10m", blocked, wait)
	}
	if _, blocked := l.blocked("other", t0.Add(3*time.Minute)); blocked {
		t.Error("another key was blocked")
	}
}

func TestHitsAgeOutOneAtATime(t *testing.T) {
	l := newLimiter(2, 10*time.Minute)
	l.hit("k", t0)
	l.hit("k", t0.Add(4*time.Minute))
	if _, blocked := l.blocked("k", t0.Add(9*time.Minute)); !blocked {
		t.Error("should still be blocked at 9m")
	}
	if _, blocked := l.blocked("k", t0.Add(10*time.Minute)); blocked {
		t.Error("the first hit is out of the window at exactly 10m: one hit left, not blocked")
	}
	if _, blocked := l.blocked("k", t0.Add(15*time.Minute)); blocked {
		t.Error("everything is out of the window at 15m")
	}
	if len(l.hits) != 0 {
		t.Errorf("%d keys kept after everything aged out, want none", len(l.hits))
	}
}

func TestResetForgetsAKey(t *testing.T) {
	l := newLimiter(1, time.Hour)
	l.hit("k", t0)
	l.reset("k")
	if _, blocked := l.blocked("k", t0); blocked {
		t.Error("still blocked after a reset")
	}
}

func TestAFullTableMakesRoomWithoutForgettingAKeyThatIsBeingHammered(t *testing.T) {
	l := newLimiter(2, time.Hour)
	l.maxKeys = 3
	l.hit("victim", t0)
	l.hit("victim", t0.Add(time.Second))
	for i := range 20 { // an attacker floods the table with other keys
		l.hit(fmt.Sprintf("noise-%d", i), t0.Add(time.Duration(i+2)*time.Second))
		l.hit("victim", t0.Add(time.Duration(i+2)*time.Second+time.Millisecond)) // and the victim keeps being tried
	}
	if len(l.hits) > 3 {
		t.Errorf("%d keys held, want at most 3", len(l.hits))
	}
	if _, blocked := l.blocked("victim", t0.Add(time.Minute)); !blocked {
		t.Error("the limit on a key under attack was lost when the table filled up")
	}
}

func TestAFullTableDropsKeysThatAgedOutFirst(t *testing.T) {
	l := newLimiter(2, time.Minute)
	l.maxKeys = 2
	l.hit("old-1", t0)
	l.hit("old-2", t0)
	l.hit("new", t0.Add(5*time.Minute)) // both old keys aged out, so there is room
	if _, ok := l.hits["new"]; !ok || len(l.hits) != 1 {
		t.Errorf("keys = %v, want only the new one", l.hits)
	}
}

func TestAFullTableThatCanMakeRoomByForgettingExpiredKeysForgetsNothingElse(t *testing.T) {
	l := newLimiter(2, time.Hour)
	l.maxKeys = 3
	l.hit("a", t0)
	l.hit("b", t0.Add(50*time.Minute))
	l.hit("c", t0.Add(51*time.Minute))
	l.hit("d", t0.Add(61*time.Minute)) // "a" has aged out, which makes room
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := l.hits[k]; !ok {
			t.Errorf("key %q was forgotten although there was room", k)
		}
	}
}

func TestHittingAKeyThatIsAlreadyKnownNeverMakesRoomByForgettingAnother(t *testing.T) {
	l := newLimiter(5, time.Hour)
	l.maxKeys = 3
	l.hit("a", t0)
	l.hit("b", t0.Add(time.Minute))
	l.hit("c", t0.Add(2*time.Minute))
	l.hit("c", t0.Add(3*time.Minute)) // the table is full, but "c" is already in it
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := l.hits[k]; !ok {
			t.Errorf("key %q was forgotten to make room for a key that needed none", k)
		}
	}
}
