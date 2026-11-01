package ports

import (
	"context"
	"errors"

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
