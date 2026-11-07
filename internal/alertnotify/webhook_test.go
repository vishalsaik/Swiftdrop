package alertnotify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swiftdrop/internal/domain"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

// secretPath stands for the part of a real webhook address that is the secret.
const secretPath = "/services/T000/B000/s3cr3t-token"

func alert() domain.Alert {
	return domain.Alert{
		ID: 7, Kind: domain.AlertUnrecordedCharge, Subject: "pay-1", OrderID: "o-9",
		Message: "provider says money moved for payment pay-1", Occurrences: 1, CreatedAt: t0, LastSeenAt: t0,
	}
}

// receiver is a server that records what was posted to it.
type receiver struct {
	srv     *httptest.Server
	got     chan *http.Request
	bodies  chan []byte
	status  int
	reply   string
	handled atomic.Int32
}

func newReceiver(t *testing.T, status int, reply string) *receiver {
	t.Helper()
	r := &receiver{got: make(chan *http.Request, 10), bodies: make(chan []byte, 10), status: status, reply: reply}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.handled.Add(1)
		b, _ := io.ReadAll(req.Body)
		r.got <- req
		r.bodies <- b
		w.WriteHeader(r.status)
		_, _ = io.WriteString(w, r.reply)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) hook(t *testing.T) *Webhook {
	t.Helper()
	w, err := NewWebhook(r.srv.URL + secretPath)
	if err != nil {
		t.Fatal(err)
	}
	w.Now = func() time.Time { return t0.Add(26 * time.Hour) }
	return w
}

// ---------------------------------------------------------------------------
// the address
// ---------------------------------------------------------------------------

func TestOnlyAnHTTPSAddressOrThisMachineIsAccepted(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://hooks.slack.com/services/T/B/x": true,
		"https://example.com":                    true,
		"http://localhost:9000/x":                true,
		"http://127.0.0.1:9000/x":                true,
		"http://[::1]:9000/x":                    true,
		"http://hooks.slack.com/services/T/B/x":  false,
		"http://10.0.0.5/x":                      false,
		"ftp://example.com/x":                    false,
		"hooks.slack.com/services":               false,
		"":                                       false,
		"https://":                               false,
		"://bad":                                 false,
	} {
		_, err := NewWebhook(raw)
		if (err == nil) != ok {
			t.Errorf("NewWebhook(%q): err = %v, want accepted = %v", raw, err, ok)
		}
	}
}

func TestARefusedAddressIsNeverRepeatedInTheError(t *testing.T) {
	for _, raw := range []string{
		"http://hooks.example.com" + secretPath, "ftp://hooks.example.com" + secretPath, "://hooks.example.com" + secretPath,
	} {
		_, err := NewWebhook(raw)
		if err == nil {
			t.Fatalf("%q was accepted", raw)
		}
		if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "hooks.example.com") {
			t.Errorf("the error repeats the address: %v", err)
		}
	}
}

func TestOnlyTheHostIsAvailableForALogLine(t *testing.T) {
	w, err := NewWebhook("https://hooks.slack.com" + secretPath)
	if err != nil || w.Host() != "hooks.slack.com" {
		t.Errorf("host = %q, %v", w.Host(), err)
	}
}

// ---------------------------------------------------------------------------
// what is sent
// ---------------------------------------------------------------------------

func TestTheAlertIsPostedAsJSONWithATextAndTheStructuredFields(t *testing.T) {
	r := newReceiver(t, 200, "ok")
	w := r.hook(t)
	w.Dashboard = "https://dispatch.example.com/"
	a := alert()
	a.Occurrences = 3

	if err := w.Send(context.Background(), Message{Alert: a}); err != nil {
		t.Fatal(err)
	}
	req, raw := <-r.got, <-r.bodies
	if req.Method != http.MethodPost || req.URL.Path != secretPath {
		t.Errorf("%s %s", req.Method, req.URL.Path)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if ua := req.Header.Get("User-Agent"); !strings.HasPrefix(ua, "swiftdrop-alerts/") {
		t.Errorf("User-Agent = %q", ua)
	}
	var got body
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	for _, part := range []string{
		"SwiftDrop: Money taken, but not written down", "(order o-9)", "provider says money moved for payment pay-1",
		"Seen 3 times.", "https://dispatch.example.com/",
	} {
		if !strings.Contains(got.Text, part) {
			t.Errorf("the text lacks %q:\n%s", part, got.Text)
		}
	}
	if strings.Contains(got.Text, "Reminder") || got.Reminder {
		t.Errorf("a first message is not a reminder: %+v", got)
	}
	if got.Alert.ID != 7 || got.Alert.Kind != domain.AlertUnrecordedCharge || got.Alert.Title != "Money taken, but not written down" ||
		got.Alert.Subject != "pay-1" || got.Alert.OrderID != "o-9" || got.Alert.Occurrences != 3 || !got.Alert.CreatedAt.Equal(t0) {
		t.Errorf("alert = %+v", got.Alert)
	}
}

func TestAReminderSaysHowLongTheAlertHasBeenOpen(t *testing.T) {
	r := newReceiver(t, 200, "")
	w := r.hook(t) // 26 hours after the alert was made
	if err := w.Send(context.Background(), Message{Alert: alert(), Reminder: true}); err != nil {
		t.Fatal(err)
	}
	<-r.got
	var got body
	_ = json.Unmarshal(<-r.bodies, &got)
	if !strings.HasPrefix(got.Text, "Reminder, still open after 26 hours: SwiftDrop:") || !got.Reminder {
		t.Errorf("text = %q, reminder = %v", got.Text, got.Reminder)
	}
}

func TestTheTextLeavesOutWhatIsNotThere(t *testing.T) {
	w, _ := NewWebhook("https://example.com/x")
	a := alert()
	a.OrderID, a.Occurrences = "", 1
	text := w.text(Message{Alert: a})
	if strings.Contains(text, "order") || strings.Contains(text, "Seen") || strings.Count(text, "\n") != 1 {
		t.Errorf("text = %q", text)
	}
	a.Kind = "something_new"
	if got := w.text(Message{Alert: a}); !strings.Contains(got, "SwiftDrop: something new") {
		t.Errorf("an unknown kind reads as %q", got)
	}
}

func TestAgesAreRoughAndReadable(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0 minutes", time.Minute: "1 minute", 40 * time.Minute: "40 minutes", time.Hour: "1 hour",
		5 * time.Hour: "5 hours", 47 * time.Hour: "47 hours", 48 * time.Hour: "2 days", 100 * time.Hour: "4 days",
	} {
		if got := age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// what comes back
// ---------------------------------------------------------------------------

func TestAnyTwoHundredSeriesAnswerIsDelivered(t *testing.T) {
	for _, status := range []int{200, 201, 204, 299} {
		r := newReceiver(t, status, "")
		if err := r.hook(t).Send(context.Background(), Message{Alert: alert()}); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
	}
}

func TestAnythingElseIsAFailureThatSaysWhatTheServerSaid(t *testing.T) {
	for _, status := range []int{300, 400, 404, 429, 500, 503} {
		r := newReceiver(t, status, "  invalid_token \n  try again ")
		err := r.hook(t).Send(context.Background(), Message{Alert: alert()})
		if err == nil || !strings.Contains(err.Error(), "invalid_token try again") {
			t.Errorf("status %d: err = %v", status, err)
		}
		if err != nil && strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("status %d: the error repeats the address: %v", status, err)
		}
	}
}

func TestALongReplyIsCutForTheLog(t *testing.T) {
	r := newReceiver(t, 500, strings.Repeat("x", 5000))
	err := r.hook(t).Send(context.Background(), Message{Alert: alert()})
	if err == nil || len(err.Error()) > 300 {
		t.Errorf("err = %v (%d bytes)", err, len(err.Error()))
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	elsewhere := newReceiver(t, 200, "")
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, elsewhere.srv.URL+"/stolen", http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	w, _ := NewWebhook(redirecting.URL + secretPath)

	if err := w.Send(context.Background(), Message{Alert: alert()}); err == nil {
		t.Error("a redirect was treated as delivered")
	}
	if elsewhere.handled.Load() != 0 {
		t.Error("the message was sent on to where the redirect pointed")
	}
}

func TestAConnectionThatFailsNeverRepeatsTheAddress(t *testing.T) {
	r := newReceiver(t, 200, "")
	w := r.hook(t)
	r.srv.Close() // nothing listens any more
	err := w.Send(context.Background(), Message{Alert: alert()})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), secretPath) {
		t.Errorf("the error repeats the address: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "alert webhook:") {
		t.Errorf("err = %v", err)
	}
}

func TestACancelledSendIsRecognisableAsSuch(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body) // reading the body is what lets the server notice the client leaving
		<-req.Context().Done()
	}))
	t.Cleanup(slow.Close)
	w, _ := NewWebhook(slow.URL + secretPath)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	err := w.Send(ctx, Message{Alert: alert()})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled so the job can tell a shutdown from a failure", err)
	}
	if err != nil && strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("the error repeats the address: %v", err)
	}
}

func TestOneLineSqueezesAndCuts(t *testing.T) {
	if got := oneLine("  a \n\t b  c "); got != "a b c" {
		t.Errorf("oneLine = %q", got)
	}
	if got := oneLine(strings.Repeat("y", 201)); len(got) != 203 || !strings.HasSuffix(got, "...") {
		t.Errorf("oneLine of a long text = %d bytes", len(got))
	}
	if got := oneLine(strings.Repeat("y", 200)); len(got) != 200 {
		t.Errorf("200 bytes were cut to %d", len(got))
	}
}

// A Webhook is made by NewWebhook, which checks the address; one made by hand with a bad
// one fails cleanly, and still without repeating the address.
func TestARequestThatCannotBeBuiltIsAFailureNotAPanic(t *testing.T) {
	w := &Webhook{url: "http://[::1" + secretPath, client: http.DefaultClient, Now: time.Now}
	err := w.Send(context.Background(), Message{Alert: alert()})
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("err = %v", err)
	}
}
