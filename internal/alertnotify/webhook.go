package alertnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"swiftdrop/internal/domain"
)

const (
	webhookTimeout = 10 * time.Second
	// maxReplyRead is how much of an error reply is read, for the log.
	maxReplyRead = 512
)

// Webhook is a Sink that POSTs each message as JSON to one address. The body has a
// "text" field, which is what Slack's incoming webhooks read (and Discord's, at its
// address with /slack added), and the alert itself as structured fields for anything
// else that wants them.
//
// The address is a secret: whoever has it can post to the channel. It is never logged,
// and never part of an error.
type Webhook struct {
	url    string
	host   string
	client *http.Client
	// Dashboard, if set, is added to each message as where to look.
	Dashboard string
	// Now is the clock, for tests.
	Now func() time.Time
}

// NewWebhook checks the address and returns a Webhook. It must be https; plain http is
// accepted only for this machine, so a test can listen without a certificate. The error
// does not repeat the address.
func NewWebhook(raw string) (*Webhook, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("the alert webhook address is not a URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	default:
		return nil, errors.New("the alert webhook address must be https (plain http only for localhost)")
	}
	return &Webhook{
		url: raw, host: u.Hostname(), Now: time.Now,
		client: &http.Client{
			Timeout: webhookTimeout,
			// An address that redirects is not followed: the reply is then not a 2xx and the
			// send is a failure, instead of the message going somewhere nobody chose.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Host is the host the webhook posts to, for a log line. The rest of the address (its
// path is the secret) is not available.
func (w *Webhook) Host() string { return w.host }

type alertBody struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Subject     string    `json:"subject"`
	OrderID     string    `json:"orderId,omitempty"`
	Message     string    `json:"message"`
	Occurrences int       `json:"occurrences"`
	CreatedAt   time.Time `json:"createdAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

type body struct {
	Text     string    `json:"text"`
	Reminder bool      `json:"reminder"`
	Alert    alertBody `json:"alert"`
}

// Send posts the message. Any 2xx is delivered; anything else, a timeout or a refused
// connection is an error.
func (w *Webhook) Send(ctx context.Context, m Message) error {
	a := m.Alert
	payload, err := json.Marshal(body{
		Text: w.text(m), Reminder: m.Reminder,
		Alert: alertBody{
			ID: a.ID, Kind: a.Kind, Title: domain.AlertTitle(a.Kind), Subject: a.Subject, OrderID: a.OrderID,
			Message: a.Message, Occurrences: a.Occurrences, CreatedAt: a.CreatedAt, LastSeenAt: a.LastSeenAt,
		},
	})
	if err != nil {
		return fmt.Errorf("alert webhook: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(payload))
	if err != nil {
		return errors.New("alert webhook: could not build the request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "swiftdrop-alerts/1")

	res, err := w.client.Do(req)
	if err != nil {
		// A *url.Error carries the whole address; keep only what went wrong.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("alert webhook: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxReplyRead))
		return nil
	}
	reply, _ := io.ReadAll(io.LimitReader(res.Body, maxReplyRead))
	return fmt.Errorf("alert webhook answered %d %s", res.StatusCode, oneLine(string(reply)))
}

// text is the message a person reads.
func (w *Webhook) text(m Message) string {
	a := m.Alert
	var b strings.Builder
	if m.Reminder {
		fmt.Fprintf(&b, "Reminder, still open after %s: ", age(w.Now().Sub(a.CreatedAt)))
	}
	b.WriteString("SwiftDrop: ")
	b.WriteString(domain.AlertTitle(a.Kind))
	if a.OrderID != "" {
		fmt.Fprintf(&b, " (order %s)", a.OrderID)
	}
	b.WriteString("\n")
	b.WriteString(a.Message)
	if a.Occurrences > 1 {
		fmt.Fprintf(&b, "\nSeen %d times.", a.Occurrences)
	}
	if w.Dashboard != "" {
		fmt.Fprintf(&b, "\n%s", w.Dashboard)
	}
	return b.String()
}

// age is "40 minutes", "5 hours" or "3 days": rough is fine in a reminder.
func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// oneLine squeezes a reply into one short line for a log.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
