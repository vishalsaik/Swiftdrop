package ports

import (
	"context"
	"errors"
	"time"

	"swiftdrop/internal/domain"
)

var (
	// ErrOfferGone means the offer is not there to answer: it never existed, is not the driver's, has already
	// been answered, has run out, or its order no longer needs a driver (it was cancelled, say). Nothing was
	// changed. A driver is told the same thing in every case, so offers cannot be probed.
	ErrOfferGone = errors.New("offer is no longer open")
	// ErrDriverBusy means the driver already holds an unfinished delivery, so they cannot take another. Nothing
	// was changed.
	ErrDriverBusy = errors.New("driver already has a delivery")
)

// DispatchStore is what dispatch needs from storage. An order that is being prepared and has no driver is
// offered to one driver at a time. An offer is a lease taken with the database's clock, so any number of
// servers can run the job at once: of several offers made at the same moment for one order, or to one
// driver, exactly one is made, and a lease that ran out is never completed by the driver who held it.
type DispatchStore interface {
	// ExpireOffers ends every open offer whose lease has run out, or whose order no longer needs a driver
	// (it is not being prepared, or already has one), and answers how many it ended. It runs first in every
	// round, so the order they held is free to be offered again.
	ExpireOffers(ctx context.Context) (int, error)

	// ListWaitingOrders returns up to limit orders that need a driver and are not on offer, oldest first. An
	// order whose restaurant has no place on the map cannot be dispatched and is not listed.
	ListWaitingOrders(ctx context.Context, limit int) ([]domain.WaitingOrder, error)

	// ListCandidates returns the drivers who could be offered the order now: online and not disabled, with a
	// point sent no longer ago than maxLocationAge, holding no unfinished delivery, with no open offer, and
	// not already offered this order (a driver who declined it, or let it lapse, is not asked again).
	ListCandidates(ctx context.Context, orderID string, maxLocationAge time.Duration) ([]domain.Candidate, error)

	// OfferOrder offers the order to the driver with a lease of the given length, by the database clock. ok =
	// false means it was not made because something changed since the list was read (someone else offered the
	// order or took the driver, the order moved on, the driver went offline or is busy): the caller simply
	// moves on. Of several simultaneous offers for one order, or to one driver, exactly one gets ok = true.
	OfferOrder(ctx context.Context, orderID, driverID string, lease time.Duration) (offer domain.Offer, ok bool, err error)

	// AcceptOffer is the driver taking the order: the offer ends as accepted and the order's driver is set,
	// in one transaction. ErrOfferGone if the offer is not the driver's, not open, past its lease or its order
	// no longer needs a driver; ErrDriverBusy if they already hold an unfinished delivery. Of two accepts of
	// one offer exactly one succeeds.
	AcceptOffer(ctx context.Context, offerID int64, driverID string) (domain.Order, error)

	// DeclineOffer is the driver saying no. ErrOfferGone as for AcceptOffer.
	DeclineOffer(ctx context.Context, offerID int64, driverID string) error

	// OpenOffer returns the driver's one open offer that is still within its lease, ok = false if there is
	// none.
	OpenOffer(ctx context.Context, driverID string) (offer domain.Offer, ok bool, err error)

	// ListOffers returns every offer made for the order, oldest first: the history of who was asked and what
	// came of it.
	ListOffers(ctx context.Context, orderID string) ([]domain.Offer, error)
}
