package alertnotify

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

type markedFailed struct {
	id   int64
	wait time.Duration
}

type fakeStore struct {
	mu     sync.Mutex
	claims []ports.AlertClaim

	claimErr   error
	notifyErr  error
	failedErr  error
	gotLimit   int
	gotLease   time.Duration
	gotRemind  time.Duration
	notified   []int64
	failed     []markedFailed
	markCtxErr []error // the state of the context each mark was made with
}

func (f *fakeStore) ClaimAlertsToNotify(_ context.Context, limit int, lease, reminder time.Duration) ([]ports.AlertClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotLimit, f.gotLease, f.gotRemind = limit, lease, reminder
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	out := f.claims
	f.claims = nil
	return out, nil
}

func (f *fakeStore) MarkAlertNotified(ctx context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markCtxErr = append(f.markCtxErr, ctx.Err())
	if f.notifyErr != nil {
		return f.notifyErr
	}
	f.notified = append(f.notified, id)
	return nil
}

func (f *fakeStore) MarkAlertNotifyFailed(ctx context.Context, id int64, wait time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markCtxErr = append(f.markCtxErr, ctx.Err())
	if f.failedErr != nil {
		return f.failedErr
	}
	f.failed = append(f.failed, markedFailed{id, wait})
	return nil
}

type fakeSink struct {
	mu   sync.Mutex
	sent []Message
	// failFor lists alert ids whose send fails.
	failFor map[int64]error
	// onSend runs inside Send, to act in the middle of a send.
	onSend func(ctx context.Context, m Message) error
}

func (s *fakeSink) Send(ctx context.Context, m Message) error {
	s.mu.Lock()
	s.sent = append(s.sent, m)
	err := s.failFor[m.Alert.ID]
	hook := s.onSend
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, m)
	}
	return err
}

func claim(id int64, reminder bool, failures int) ports.AlertClaim {
	return ports.AlertClaim{
		Alert:    domain.Alert{ID: id, Kind: domain.AlertRefundGivenUp, Subject: "pay", Message: "m", Occurrences: 1},
		Reminder: reminder, Failures: failures,
	}
}

// syncBuf is a log buffer the job's goroutine can write to while a test reads it.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

// ---------------------------------------------------------------------------
// one run
// ---------------------------------------------------------------------------

func TestEveryClaimedAlertIsSentOnceAndRecorded(t *testing.T) {
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0), claim(2, true, 0)}}
	sink := &fakeSink{}
	svc := NewService(store, sink)

	rep, err := svc.RunOnce(context.Background())
	if err != nil || rep != (Report{Sent: 2}) {
		t.Fatalf("report = %+v, %v, want 2 sent", rep, err)
	}
	if len(sink.sent) != 2 || sink.sent[0].Alert.ID != 1 || sink.sent[0].Reminder || sink.sent[1].Alert.ID != 2 || !sink.sent[1].Reminder {
		t.Errorf("sent = %+v: want alert 1 (new) then alert 2 (a reminder)", sink.sent)
	}
	if len(store.notified) != 2 || store.notified[0] != 1 || store.notified[1] != 2 || len(store.failed) != 0 {
		t.Errorf("recorded sent %v, failed %v", store.notified, store.failed)
	}
}

func TestTheStoreIsAskedForTheConfiguredLeaseReminderAndBatch(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, &fakeSink{})
	svc.Lease, svc.Reminder, svc.BatchSize = 90*time.Second, 6*time.Hour, 7
	if _, err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.gotLimit != 7 || store.gotLease != 90*time.Second || store.gotRemind != 6*time.Hour {
		t.Errorf("claimed with %d, %v, %v", store.gotLimit, store.gotLease, store.gotRemind)
	}
	d := NewService(store, &fakeSink{})
	if d.Lease != time.Minute || d.Reminder != 24*time.Hour || d.BatchSize != 20 || d.BackoffBase != 30*time.Second || d.BackoffMax != 15*time.Minute {
		t.Errorf("defaults = %v %v %d %v %v", d.Lease, d.Reminder, d.BatchSize, d.BackoffBase, d.BackoffMax)
	}
}

func TestNothingDueIsNothingDone(t *testing.T) {
	sink := &fakeSink{}
	rep, err := NewService(&fakeStore{}, sink).RunOnce(context.Background())
	if err != nil || rep != (Report{}) || len(sink.sent) != 0 {
		t.Errorf("report = %+v, %v, sent %d", rep, err, len(sink.sent))
	}
}

func TestAFailedSendWaitsLongerEachTimeAndDoesNotStopTheOthers(t *testing.T) {
	logs := captureLog(t)
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0), claim(2, false, 2), claim(3, false, 0)}}
	sink := &fakeSink{failFor: map[int64]error{2: errors.New("alert webhook answered 500 oops")}}
	svc := NewService(store, sink)

	rep, err := svc.RunOnce(context.Background())
	if err != nil || rep != (Report{Sent: 2, Failed: 1}) {
		t.Fatalf("report = %+v, %v, want 2 sent and 1 failed", rep, err)
	}
	if len(store.notified) != 2 || store.notified[0] != 1 || store.notified[1] != 3 {
		t.Errorf("recorded sent %v, want 1 and 3: one failure must not stop the rest", store.notified)
	}
	// alert 2 had failed twice before, so this is the third failure: 30s, 60s, then 120s
	if len(store.failed) != 1 || store.failed[0].id != 2 || store.failed[0].wait != 2*time.Minute {
		t.Errorf("failed = %+v, want alert 2 held for 2m", store.failed)
	}
	for _, part := range []string{"alert 2", "attempt 3", "500 oops", "2m0s"} {
		if !strings.Contains(logs.String(), part) {
			t.Errorf("the log lacks %q:\n%s", part, logs)
		}
	}
}

func TestTheWaitDoublesFromTheBaseUpToTheMaximum(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeSink{})
	for failures, want := range map[int]time.Duration{
		1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute, 5: 8 * time.Minute,
		6: 15 * time.Minute, 7: 15 * time.Minute, 50: 15 * time.Minute,
	} {
		if got := svc.backoff(failures); got != want {
			t.Errorf("backoff(%d) = %v, want %v", failures, got, want)
		}
	}
	svc.BackoffBase, svc.BackoffMax = 10*time.Second, 25*time.Second
	if got := svc.backoff(2); got != 20*time.Second {
		t.Errorf("backoff(2) = %v, want 20s", got)
	}
	if got := svc.backoff(3); got != 25*time.Second {
		t.Errorf("backoff(3) = %v, want the 25s maximum, not 40s", got)
	}
}

func TestUnreadableDueAlertsAreAnError(t *testing.T) {
	store := &fakeStore{claimErr: errors.New("connection refused")}
	rep, err := NewService(store, &fakeSink{}).RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "connection refused") || rep != (Report{}) {
		t.Errorf("report = %+v, err = %v", rep, err)
	}
}

func TestAnOutcomeThatCannotBeRecordedIsCountedAndLogged(t *testing.T) {
	logs := captureLog(t)
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0), claim(2, false, 0)}, notifyErr: errors.New("db down")}
	sink := &fakeSink{failFor: map[int64]error{2: errors.New("nope")}}
	store.failedErr = errors.New("db still down")

	rep, err := NewService(store, sink).RunOnce(context.Background())
	if err != nil || rep != (Report{Errors: 2}) {
		t.Fatalf("report = %+v, %v, want 2 errors", rep, err)
	}
	if !strings.Contains(logs.String(), "will go out again") || !strings.Contains(logs.String(), "db still down") {
		t.Errorf("the log did not say so:\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// shutting down
// ---------------------------------------------------------------------------

func TestAMessageThatWentOutIsRecordedEvenWhileShuttingDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0)}}
	sink := &fakeSink{onSend: func(context.Context, Message) error {
		cancel() // told to stop just as it was delivered
		return nil
	}}
	rep, err := NewService(store, sink).RunOnce(ctx)
	if err != nil || rep.Sent != 1 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	if len(store.notified) != 1 || len(store.markCtxErr) != 1 || store.markCtxErr[0] != nil {
		t.Errorf("recorded %v with a context that was %v: it must not be the cancelled one, or the alert would be sent again at the next start",
			store.notified, store.markCtxErr)
	}
}

func TestAnInterruptedSendIsNotAFailureOfTheAlert(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0), claim(2, false, 0)}}
	sink := &fakeSink{onSend: func(ctx context.Context, m Message) error {
		cancel()
		return ctx.Err() // the request was cut short
	}}
	rep, err := NewService(store, sink).RunOnce(ctx)
	if err != nil || rep != (Report{}) {
		t.Fatalf("report = %+v, %v, want nothing counted", rep, err)
	}
	if len(store.notified) != 0 || len(store.failed) != 0 {
		t.Errorf("recorded sent %v and failed %v: an interrupted send leaves the alert to its lease", store.notified, store.failed)
	}
	if len(sink.sent) != 1 {
		t.Errorf("%d sends after being told to stop, want only the one in progress", len(sink.sent))
	}
}

func TestTheRestOfABatchIsLeftWhenToldToStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0), claim(2, false, 0), claim(3, false, 0)}}
	sink := &fakeSink{onSend: func(context.Context, Message) error { cancel(); return nil }}
	rep, _ := NewService(store, sink).RunOnce(ctx)
	if rep.Sent != 1 || len(sink.sent) != 1 || len(store.notified) != 1 {
		t.Errorf("report = %+v, sent %d: want only the first, the others wait for their lease", rep, len(sink.sent))
	}
}

// A failure of the sink that merely happens while shutting down is still recorded as a
// failure when it is not the context's own error.
func TestARealFailureDuringShutdownIsStillRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0)}}
	sink := &fakeSink{onSend: func(context.Context, Message) error { cancel(); return errors.New("answered 500") }}
	rep, _ := NewService(store, sink).RunOnce(ctx)
	if rep.Failed != 1 || len(store.failed) != 1 {
		t.Errorf("report = %+v, failed %v", rep, store.failed)
	}
}

// ---------------------------------------------------------------------------
// the loop
// ---------------------------------------------------------------------------

func TestTheLoopSendsNowAndOnEveryTickAndStops(t *testing.T) {
	logs := captureLog(t)
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0)}}
	sink := &fakeSink{}
	svc := NewService(store, sink)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx, 20*time.Millisecond) }()

	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return len(store.notified) == 1 })
	store.mu.Lock()
	store.claims = []ports.AlertClaim{claim(2, false, 0)} // a new alert appears between runs
	store.mu.Unlock()
	waitFor(t, func() bool { store.mu.Lock(); defer store.mu.Unlock(); return len(store.notified) == 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
	if !strings.Contains(logs.String(), "run done: 1 sent") {
		t.Errorf("a run that sent something was not logged:\n%s", logs)
	}
}

func TestTheLoopLogsAFailureToReadButNotOneCausedByStopping(t *testing.T) {
	logs := captureLog(t)
	store := &fakeStore{claimErr: errors.New("connection refused")}
	svc := NewService(store, &fakeSink{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx, 10*time.Millisecond) }()
	waitFor(t, func() bool { return strings.Contains(logs.String(), "connection refused") })
	cancel()
	<-done
}

func TestTheLoopDoesNotRunWithoutAPositiveInterval(t *testing.T) {
	logs := captureLog(t)
	store := &fakeStore{claims: []ports.AlertClaim{claim(1, false, 0)}}
	NewService(store, &fakeSink{}).Run(context.Background(), 0) // returns at once
	if len(store.notified) != 0 || !strings.Contains(logs.String(), "interval must be positive") {
		t.Errorf("sent %v, log %q", store.notified, logs)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

// Being told to stop can make a read fail; that is not worth a log line.
func TestAReadThatFailsBecauseWeAreStoppingIsNotLogged(t *testing.T) {
	logs := captureLog(t)
	store := &fakeStore{claimErr: errors.New("context canceled while reading")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewService(store, &fakeSink{}).Run(ctx, time.Hour) // runs once, sees it is stopped, returns
	if strings.Contains(logs.String(), "context canceled while reading") {
		t.Errorf("a failure caused by stopping was logged:\n%s", logs)
	}
}
