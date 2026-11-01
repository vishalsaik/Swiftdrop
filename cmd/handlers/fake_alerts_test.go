package handlers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// The alert half of fakeDB. It follows the Postgres adapter's contract: one open
// alert per (kind, subject), raising it again counts an occurrence, newest
// activity first, and the same sentinel errors.

func (f *fakeDB) RaiseAlert(ctx context.Context, kind, subject, orderID, message string) error {
	f.mu.Lock()
	block := f.alertBlock
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil { // like a database call on a cancelled context
		return err
	}
	if f.alertErr != nil {
		return f.alertErr
	}
	if kind == "" || subject == "" || message == "" {
		return fmt.Errorf("kind, subject and message are required")
	}
	now := time.Now()
	for i, a := range f.alerts {
		if a.Kind == kind && a.Subject == subject && a.Open() {
			f.alerts[i].Occurrences++
			f.alerts[i].LastSeenAt, f.alerts[i].Message = now, message
			if orderID != "" {
				f.alerts[i].OrderID = orderID
			}
			return nil
		}
	}
	f.nextAlertID++
	f.alerts = append(f.alerts, domain.Alert{
		ID: f.nextAlertID, Kind: kind, Subject: subject, OrderID: orderID, Message: message,
		Occurrences: 1, CreatedAt: now, LastSeenAt: now,
	})
	return nil
}

func (f *fakeDB) ListAlerts(_ context.Context, openOnly bool, limit int) ([]domain.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alertErr != nil {
		return nil, f.alertErr
	}
	out := []domain.Alert{}
	for _, a := range f.alerts {
		if !openOnly || a.Open() {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeDB) ResolveAlert(_ context.Context, id int64, note string) (domain.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alertErr != nil {
		return domain.Alert{}, f.alertErr
	}
	for i, a := range f.alerts {
		if a.ID != id {
			continue
		}
		if !a.Open() {
			return domain.Alert{}, fmt.Errorf("%w: %d", ports.ErrAlertResolved, id)
		}
		now := time.Now()
		f.alerts[i].ResolvedAt, f.alerts[i].ResolvedNote = &now, note
		return f.alerts[i], nil
	}
	return domain.Alert{}, fmt.Errorf("%w: %d", ports.ErrAlertNotFound, id)
}

// openAlerts returns the alerts nobody has resolved, for assertions.
func (f *fakeDB) openAlerts() []domain.Alert {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Alert
	for _, a := range f.alerts {
		if a.Open() {
			out = append(out, a)
		}
	}
	return out
}
