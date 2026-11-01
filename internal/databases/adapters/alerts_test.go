package adapters

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// These need the same Postgres as the other adapter tests. Their alerts all have a
// subject starting with "al-", which is how they are found and cleaned up.

func alertDB(t *testing.T) *DBAdapter {
	t.Helper()
	db := testAdapter(t)
	clean := func() { _, _ = db.Pool.Exec(context.Background(), "DELETE FROM alerts WHERE subject LIKE 'al-%'") }
	clean()
	t.Cleanup(clean)
	return db
}

// ours lists the alerts these tests made (open and resolved), newest activity first.
func ours(t *testing.T, db *DBAdapter, openOnly bool) []domain.Alert {
	t.Helper()
	all, err := db.ListAlerts(context.Background(), openOnly, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.Alert
	for _, a := range all {
		if len(a.Subject) >= 3 && a.Subject[:3] == "al-" {
			out = append(out, a)
		}
	}
	return out
}

func TestRaisingAnAlertOpensIt(t *testing.T) {
	db := alertDB(t)
	if err := db.RaiseAlert(context.Background(), domain.AlertUnrecordedCharge, "al-pay-1", "o1", "money moved, no record"); err != nil {
		t.Fatal(err)
	}
	got := ours(t, db, true)
	if len(got) != 1 {
		t.Fatalf("%d alerts, want 1", len(got))
	}
	a := got[0]
	if a.Kind != domain.AlertUnrecordedCharge || a.Subject != "al-pay-1" || a.OrderID != "o1" || a.Message != "money moved, no record" ||
		a.Occurrences != 1 || !a.Open() || a.ID == 0 || a.CreatedAt.IsZero() || a.LastSeenAt.IsZero() {
		t.Errorf("alert = %+v", a)
	}
}

// The same problem raised again is one alert with a bigger count and the newest
// message, never a second row.
func TestRaisingTheSameProblemAgainCountsItInsteadOfRepeatingIt(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	if err := db.RaiseAlert(ctx, domain.AlertContradictingEvent, "al-pay-2", "o2", "first message"); err != nil {
		t.Fatal(err)
	}
	first := ours(t, db, true)[0]
	time.Sleep(20 * time.Millisecond)
	if err := db.RaiseAlert(ctx, domain.AlertContradictingEvent, "al-pay-2", "", "second message"); err != nil {
		t.Fatal(err)
	}
	got := ours(t, db, true)
	if len(got) != 1 {
		t.Fatalf("%d alerts, want the same one", len(got))
	}
	a := got[0]
	if a.ID != first.ID || a.Occurrences != 2 || a.Message != "second message" {
		t.Errorf("alert = %+v, want the same alert, 2 occurrences and the newest message", a)
	}
	if a.OrderID != "o2" {
		t.Errorf("order id = %q, an empty one on a repeat must not erase the known one", a.OrderID)
	}
	if !a.LastSeenAt.After(first.LastSeenAt) || !a.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("times: created %v -> %v, last seen %v -> %v", first.CreatedAt, a.CreatedAt, first.LastSeenAt, a.LastSeenAt)
	}
}

// A later report may know which order it is about when the first did not.
func TestARepeatThatKnowsTheOrderTeachesItToTheAlert(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	if err := db.RaiseAlert(ctx, domain.AlertUnrecordedCharge, "al-learn", "", "first, order unknown"); err != nil {
		t.Fatal(err)
	}
	if err := db.RaiseAlert(ctx, domain.AlertUnrecordedCharge, "al-learn", "o9", "second"); err != nil {
		t.Fatal(err)
	}
	if got := ours(t, db, true); len(got) != 1 || got[0].OrderID != "o9" {
		t.Errorf("alerts = %+v, want one alert that now names order o9", got)
	}
}

// Two alerts last seen at the same instant are listed newest first.
func TestAlertsSeenAtTheSameInstantAreListedNewestFirst(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	for _, subject := range []string{"al-tie-1", "al-tie-2", "al-tie-3"} {
		if err := db.RaiseAlert(ctx, domain.AlertRefundGivenUp, subject, "", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE alerts SET last_seen_at = '2026-01-01T00:00:00Z' WHERE subject LIKE 'al-tie-%'"); err != nil {
		t.Fatal(err)
	}
	got := ours(t, db, true)
	if len(got) != 3 || got[0].Subject != "al-tie-3" || got[1].Subject != "al-tie-2" || got[2].Subject != "al-tie-1" {
		t.Errorf("order = %+v, want al-tie-3, al-tie-2, al-tie-1", got)
	}
}

func TestADifferentKindOrSubjectIsADifferentAlert(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	for _, c := range [][2]string{{domain.AlertUnrecordedCharge, "al-a"}, {domain.AlertContradictingEvent, "al-a"}, {domain.AlertUnrecordedCharge, "al-b"}} {
		if err := db.RaiseAlert(ctx, c[0], c[1], "", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if got := ours(t, db, true); len(got) != 3 {
		t.Errorf("%d alerts, want 3", len(got))
	}
}

func TestAResolvedAlertIsClosedAndTheProblemCanOpenAFreshOne(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	if err := db.RaiseAlert(ctx, domain.AlertRefundGivenUp, "al-pay-3", "o3", "gave up"); err != nil {
		t.Fatal(err)
	}
	id := ours(t, db, true)[0].ID

	resolved, err := db.ResolveAlert(ctx, id, "refunded by hand in the Stripe dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Open() || resolved.ResolvedNote != "refunded by hand in the Stripe dashboard" || resolved.ID != id {
		t.Errorf("resolved = %+v", resolved)
	}
	if got := ours(t, db, true); len(got) != 0 {
		t.Errorf("%d open alerts after resolving, want none", len(got))
	}
	if got := ours(t, db, false); len(got) != 1 || got[0].Open() {
		t.Errorf("the resolved alert should still be listed with all: %+v", got)
	}

	// The same problem happening again is news: a new open alert, not the old one.
	if err := db.RaiseAlert(ctx, domain.AlertRefundGivenUp, "al-pay-3", "o3", "gave up again"); err != nil {
		t.Fatal(err)
	}
	open := ours(t, db, true)
	if len(open) != 1 || open[0].ID == id || open[0].Occurrences != 1 {
		t.Errorf("open alerts = %+v, want a fresh alert", open)
	}
}

func TestResolvingAnAlertTwiceOrAMissingOneIsRefused(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	if err := db.RaiseAlert(ctx, domain.AlertUnrecordedCharge, "al-pay-4", "", "x"); err != nil {
		t.Fatal(err)
	}
	id := ours(t, db, true)[0].ID
	if _, err := db.ResolveAlert(ctx, id, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveAlert(ctx, id, "second"); !errors.Is(err, ports.ErrAlertResolved) {
		t.Errorf("second resolve: err = %v, want ErrAlertResolved", err)
	}
	if got := ours(t, db, false)[0].ResolvedNote; got != "first" {
		t.Errorf("note = %q, the second resolve overwrote the first", got)
	}
	if _, err := db.ResolveAlert(ctx, 1<<40, "x"); !errors.Is(err, ports.ErrAlertNotFound) {
		t.Errorf("missing alert: err = %v, want ErrAlertNotFound", err)
	}
}

func TestAlertsAreListedNewestActivityFirstAndLimited(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	for _, s := range []string{"al-1", "al-2", "al-3"} {
		if err := db.RaiseAlert(ctx, domain.AlertUnrecordedCharge, s, "", "x"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// al-1 happens again, so it is the most recent activity
	if err := db.RaiseAlert(ctx, domain.AlertUnrecordedCharge, "al-1", "", "again"); err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, a := range ours(t, db, true) {
		subjects = append(subjects, a.Subject)
	}
	if want := []string{"al-1", "al-3", "al-2"}; !slices.Equal(subjects, want) {
		t.Errorf("order = %v, want %v", subjects, want)
	}
	if got, err := db.ListAlerts(ctx, true, 1); err != nil || len(got) != 1 {
		t.Errorf("limit 1 returned %d, err %v", len(got), err)
	}
}

func TestAnAlertNeedsAKindASubjectAndAMessage(t *testing.T) {
	db := alertDB(t)
	for _, c := range [][3]string{{"", "al-x", "m"}, {"k", "", "m"}, {"k", "al-x", ""}} {
		if err := db.RaiseAlert(context.Background(), c[0], c[1], "", c[2]); err == nil {
			t.Errorf("RaiseAlert(%q, %q, %q) was accepted", c[0], c[1], c[2])
		}
	}
	if _, err := db.ListAlerts(context.Background(), true, 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}
}

// A webhook delivered many times at once is one alert that counted every delivery.
func TestTwentySimultaneousRaisesAreOneAlertWithTwentyOccurrences(t *testing.T) {
	db := alertDB(t)
	const n = 20
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = db.RaiseAlert(context.Background(), domain.AlertContradictingEvent, "al-race", "o", "same problem")
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("raise %d: %v", i, err)
		}
	}
	got := ours(t, db, true)
	if len(got) != 1 || got[0].Occurrences != n {
		t.Errorf("alerts = %+v, want one with %d occurrences", got, n)
	}
}

func TestAlertStoreFailuresAreInternalErrors(t *testing.T) {
	db := alertDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.RaiseAlert(ctx, "k", "al-ctx", "", "m"); err == nil {
		t.Error("RaiseAlert: want an error for a cancelled context")
	}
	if got, err := db.ListAlerts(ctx, true, 10); err == nil || got != nil {
		t.Errorf("ListAlerts: got %v, %v, want an error", got, err)
	}
	_, err := db.ResolveAlert(ctx, 1, "x")
	if err == nil || errors.Is(err, ports.ErrAlertNotFound) || errors.Is(err, ports.ErrAlertResolved) {
		t.Errorf("ResolveAlert: err = %v, want an internal error that is neither not-found nor resolved", err)
	}
}
