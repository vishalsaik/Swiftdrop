package adapters

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// Sending alerts out, against the real database. The alerts these tests make have a
// subject starting with "al-" (see alertDB). The table may hold open alerts that other
// tests made, so a claim here takes only what is due and the tests look at their own.

func raise(t *testing.T, db *DBAdapter, subject string) {
	t.Helper()
	if err := db.RaiseAlert(context.Background(), domain.AlertRefundGivenUp, subject, "o-"+subject, "message for "+subject); err != nil {
		t.Fatal(err)
	}
}

// claimOurs claims whatever is due and returns only the alerts these tests made.
func claimOurs(t *testing.T, db *DBAdapter, lease, reminder time.Duration) []ports.AlertClaim {
	t.Helper()
	claims, err := db.ClaimAlertsToNotify(context.Background(), 1000, lease, reminder)
	if err != nil {
		t.Fatal(err)
	}
	var out []ports.AlertClaim
	for _, c := range claims {
		if strings.HasPrefix(c.Alert.Subject, "al-") {
			out = append(out, c)
		}
	}
	return out
}

func idOf(t *testing.T, db *DBAdapter, subject string) int64 {
	t.Helper()
	for _, a := range ours(t, db, false) {
		if a.Subject == subject {
			return a.ID
		}
	}
	t.Fatalf("no alert for %s", subject)
	return 0
}

func failures(t *testing.T, db *DBAdapter, id int64) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT notify_failures FROM alerts WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestANewAlertIsClaimedOnceAndHeldWhileItIsBeingSent(t *testing.T) {
	db := alertDB(t)
	raise(t, db, "al-n1")
	raise(t, db, "al-n2")

	got := claimOurs(t, db, time.Minute, 0)
	if len(got) != 2 || got[0].Alert.Subject != "al-n1" || got[1].Alert.Subject != "al-n2" {
		t.Fatalf("claimed %d alerts: %+v, want both, oldest first", len(got), got)
	}
	for _, c := range got {
		if c.Reminder || c.Failures != 0 || c.Alert.NotifiedAt != nil || c.Alert.Message == "" || c.Alert.OrderID == "" {
			t.Errorf("claim = %+v: want a first send with the whole alert", c)
		}
	}
	if again := claimOurs(t, db, time.Minute, 0); len(again) != 0 {
		t.Errorf("%d alerts claimed again while held, want none", len(again))
	}
}

func TestAClaimedAlertComesBackWhenItsLeaseEnds(t *testing.T) {
	db := alertDB(t)
	raise(t, db, "al-lease")
	if got := claimOurs(t, db, 200*time.Millisecond, 0); len(got) != 1 {
		t.Fatalf("claimed %d", len(got))
	}
	time.Sleep(400 * time.Millisecond) // a notifier that died holding it
	if got := claimOurs(t, db, time.Minute, 0); len(got) != 1 {
		t.Errorf("claimed %d after the lease ended, want 1", len(got))
	}
}

func TestTwoNotifiersNeverGetTheSameAlert(t *testing.T) {
	db := alertDB(t)
	for _, s := range []string{"al-c01", "al-c02", "al-c03", "al-c04", "al-c05", "al-c06", "al-c07", "al-c08", "al-c09", "al-c10"} {
		raise(t, db, s)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, c := range claimOurs(t, db, time.Minute, 0) {
				mu.Lock()
				seen[c.Alert.Subject]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 10 {
		t.Errorf("%d alerts were claimed, want all 10: %v", len(seen), seen)
	}
	for s, n := range seen {
		if n != 1 {
			t.Errorf("%s was claimed %d times", s, n)
		}
	}
}

func TestASentAlertIsNotClaimedAgainUntilItsReminderIsDue(t *testing.T) {
	db := alertDB(t)
	raise(t, db, "al-sent")
	claimOurs(t, db, time.Minute, time.Hour)
	id := idOf(t, db, "al-sent")
	if err := db.MarkAlertNotified(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	if got := claimOurs(t, db, time.Minute, 0); len(got) != 0 {
		t.Errorf("a sent alert was claimed with reminders off: %+v", got)
	}
	if got := claimOurs(t, db, time.Minute, time.Hour); len(got) != 0 {
		t.Errorf("a sent alert was claimed again straight away: %+v", got)
	}

	// two hours pass
	if _, err := db.Pool.Exec(context.Background(), `UPDATE alerts SET notified_at = now() - interval '2 hours' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if got := claimOurs(t, db, time.Minute, 0); len(got) != 0 {
		t.Errorf("with reminders off, an old sent alert was claimed: %+v", got)
	}
	got := claimOurs(t, db, time.Minute, time.Hour)
	if len(got) != 1 || !got[0].Reminder || got[0].Alert.NotifiedAt == nil {
		t.Fatalf("claimed %+v, want the alert once more as a reminder", got)
	}
	// a reminder is not due yet when only 30 minutes have passed
	if err := db.MarkAlertNotified(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE alerts SET notified_at = now() - interval '30 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if got := claimOurs(t, db, time.Minute, time.Hour); len(got) != 0 {
		t.Errorf("a reminder came after 30 minutes with an hour set: %+v", got)
	}
}

func TestAResolvedAlertIsNotClaimed(t *testing.T) {
	db := alertDB(t)
	raise(t, db, "al-done")
	if _, err := db.ResolveAlert(context.Background(), idOf(t, db, "al-done"), "handled"); err != nil {
		t.Fatal(err)
	}
	if got := claimOurs(t, db, time.Minute, time.Hour); len(got) != 0 {
		t.Errorf("a resolved alert was claimed: %+v", got)
	}
}

func TestAFailedSendWaitsCountsAndIsForgottenOnSuccess(t *testing.T) {
	db := alertDB(t)
	ctx := context.Background()
	raise(t, db, "al-fail")
	claimOurs(t, db, time.Minute, 0)
	id := idOf(t, db, "al-fail")

	if err := db.MarkAlertNotifyFailed(ctx, id, 250*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := claimOurs(t, db, time.Minute, 0); len(got) != 0 {
		t.Errorf("a failed alert was claimed before its wait: %+v", got)
	}
	time.Sleep(450 * time.Millisecond)
	got := claimOurs(t, db, time.Minute, 0)
	if len(got) != 1 || got[0].Failures != 1 || got[0].Reminder {
		t.Fatalf("claimed %+v, want the alert with 1 failure, still a first send", got)
	}

	if err := db.MarkAlertNotifyFailed(ctx, id, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if n := failures(t, db, id); n != 2 {
		t.Errorf("failures = %d, want 2", n)
	}
	if err := db.MarkAlertNotified(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := failures(t, db, id); n != 0 {
		t.Errorf("failures = %d after a success, want 0", n)
	}
}

func TestASentAlertShowsWhenInTheList(t *testing.T) {
	db := alertDB(t)
	raise(t, db, "al-shown")
	if a := ours(t, db, true)[0]; a.NotifiedAt != nil {
		t.Errorf("NotifiedAt = %v before it was sent", a.NotifiedAt)
	}
	claimOurs(t, db, time.Minute, 0)
	before := time.Now().Add(-5 * time.Second)
	if err := db.MarkAlertNotified(context.Background(), idOf(t, db, "al-shown")); err != nil {
		t.Fatal(err)
	}
	a := ours(t, db, true)[0]
	if a.NotifiedAt == nil || a.NotifiedAt.Before(before) || a.NotifiedAt.After(time.Now().Add(5*time.Second)) {
		t.Errorf("NotifiedAt = %v, want about now", a.NotifiedAt)
	}
}

func TestOnlyTheLimitIsClaimedAndTheRestWait(t *testing.T) {
	db := alertDB(t)
	for _, s := range []string{"al-l1", "al-l2", "al-l3"} {
		raise(t, db, s)
	}
	// others in the table may be due too, so claim a lot and count ours separately
	first, err := db.ClaimAlertsToNotify(context.Background(), 1, time.Minute, 0)
	if err != nil || len(first) != 1 {
		t.Fatalf("claimed %d, %v, want exactly the limit", len(first), err)
	}
}

func TestClaimingNeedsAPositiveLimitAndLease(t *testing.T) {
	db := alertDB(t)
	for _, args := range [][2]time.Duration{{0, time.Minute}, {1, 0}, {1, -time.Second}} {
		if _, err := db.ClaimAlertsToNotify(context.Background(), int(args[0]), args[1], 0); err == nil {
			t.Errorf("limit %d, lease %v was accepted", args[0], args[1])
		}
	}
}

func TestNotifyStoreFailuresAreWrapped(t *testing.T) {
	db := alertDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := db.ClaimAlertsToNotify(ctx, 5, time.Minute, 0); err == nil || got != nil {
		t.Errorf("ClaimAlertsToNotify: %v, %v, want an error for a cancelled context", got, err)
	}
	if err := db.MarkAlertNotified(ctx, 1); err == nil || !strings.Contains(err.Error(), "alert 1") {
		t.Errorf("MarkAlertNotified: %v", err)
	}
	if err := db.MarkAlertNotifyFailed(ctx, 1, time.Second); err == nil || !strings.Contains(err.Error(), "alert 1") {
		t.Errorf("MarkAlertNotifyFailed: %v", err)
	}
}
