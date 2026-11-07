package ports

import (
	"context"
	"errors"
	"time"

	"swiftdrop/internal/domain"
)

var (
	ErrAlertNotFound = errors.New("alert not found")
	// ErrAlertResolved means the alert was already resolved; nothing was changed.
	ErrAlertResolved = errors.New("alert is already resolved")
)

// AlertStore keeps the problems a person has to look at.
type AlertStore interface {
	// RaiseAlert opens an alert for (kind, subject), or, if one is already open
	// for it, records another occurrence and the newest message instead of adding
	// a second. orderID may be empty.
	RaiseAlert(ctx context.Context, kind, subject, orderID, message string) error

	// ListAlerts returns up to limit alerts, newest activity first. With openOnly
	// it leaves out the resolved ones.
	ListAlerts(ctx context.Context, openOnly bool, limit int) ([]domain.Alert, error)

	// ResolveAlert closes an open alert with a note. ErrAlertNotFound if there is
	// no such alert, ErrAlertResolved if it was already closed.
	ResolveAlert(ctx context.Context, id int64, note string) (domain.Alert, error)
}

// AlertClaim is an alert a notifier holds for now, to send out.
type AlertClaim struct {
	Alert domain.Alert
	// Reminder is true when the alert was sent before and is still open.
	Reminder bool
	// Failures is how many sends of it failed in a row.
	Failures int
}

// AlertNotifyStore is what the job that sends alerts out needs. It is separate from
// AlertStore so that what raises and lists alerts does not have to know about sending.
type AlertNotifyStore interface {
	// ClaimAlertsToNotify holds up to limit open alerts that are due, each for lease:
	// the ones never sent and, when reminder is above zero, the ones last sent more
	// than reminder ago. A held alert is not given to another notifier until the lease
	// ends, so any number of servers can run the job and an alert is sent once.
	ClaimAlertsToNotify(ctx context.Context, limit int, lease, reminder time.Duration) ([]AlertClaim, error)

	// MarkAlertNotified records the alert as sent now and releases it.
	MarkAlertNotified(ctx context.Context, id int64) error

	// MarkAlertNotifyFailed counts a failed send and keeps the alert from being
	// claimed again for wait.
	MarkAlertNotifyFailed(ctx context.Context, id int64, wait time.Duration) error
}
