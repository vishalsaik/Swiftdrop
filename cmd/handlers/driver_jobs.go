package handlers

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/gorilla/mux"
)

// What a driver is offered and what they are doing. A driver sees the restaurant they would collect from and
// how far it is, and for the delivery they have accepted, where it goes. They are never shown who ordered, what
// they paid, or anything of the order but how many things there are: that is the customer's, and staff's.

// placeView is a restaurant as a driver sees it: its name and where it is.
type placeView struct {
	Name      string   `json:"name"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

// offerView is the order a driver is being asked to take. It carries no customer, address or price.
type offerView struct {
	ID int64 `json:"id"`
	// RemainingSeconds is how long the driver has to answer, by the database's clock, to count down from.
	RemainingSeconds float64   `json:"remainingSeconds"`
	Restaurant       placeView `json:"restaurant"`
	// PickupKm is how far the restaurant is from where the driver last said they were; absent if they have not.
	PickupKm *float64 `json:"pickupKm,omitempty"`
}

// dropoffView is where a delivery goes: what a driver needs to find the door. The customer's own name for the
// place ("Home") is not part of it.
type dropoffView struct {
	Line1      string  `json:"line1"`
	Landmark   string  `json:"landmark,omitempty"`
	City       string  `json:"city,omitempty"`
	PostalCode string  `json:"postalCode,omitempty"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
}

// jobView is the delivery a driver has accepted.
type jobView struct {
	OrderID    string            `json:"orderId"`
	State      domain.OrderState `json:"state"`
	FoodReady  bool              `json:"foodReady"`
	ItemCount  int               `json:"itemCount"`
	Restaurant placeView         `json:"restaurant"`
	DeliverTo  *dropoffView      `json:"deliverTo,omitempty"`
}

// errOfferGoneMessage is said for every offer that cannot be answered, whatever the reason (it never existed,
// is not theirs, was answered, ran out, or its order was cancelled), so offers cannot be probed.
const errOfferGoneMessage = "that offer is no longer open"

// placeOf reads a restaurant for a driver's view. A restaurant that cannot be read is shown without a name
// rather than failing the whole answer: the driver can still do the delivery.
func (s *Server) placeOf(ctx context.Context, restaurantID string) placeView {
	rest, err := s.DB.GetRestaurant(ctx, restaurantID)
	if err != nil {
		log.Printf("driver view: reading restaurant %s: %v", restaurantID, err)
		return placeView{}
	}
	return placeView{Name: rest.Name, Latitude: rest.Latitude, Longitude: rest.Longitude}
}

// jobViewOf is what a driver is shown of an order they are delivering.
func (s *Server) jobViewOf(ctx context.Context, o domain.Order) jobView {
	items := 0
	for _, it := range o.Items {
		items += it.Quantity
	}
	v := jobView{
		OrderID: o.ID, State: o.State, FoodReady: o.FoodReady != nil, ItemCount: items,
		Restaurant: s.placeOf(ctx, o.RestaurantID),
	}
	if a := o.DeliveryAddress; a != nil {
		v.DeliverTo = &dropoffView{Line1: a.Line1, Landmark: a.Landmark, City: a.City, PostalCode: a.PostalCode, Latitude: a.Latitude, Longitude: a.Longitude}
	}
	return v
}

// writeOrder answers with the order a request changed. Staff and customers are answered with the order; a
// driver, who may act only on their own delivery, is answered with the job and nothing more.
func (s *Server) writeOrder(w http.ResponseWriter, r *http.Request, o domain.Order) {
	switch principalFrom(r).User.Role {
	case domain.RoleDriver:
		writeJSON(w, http.StatusOK, map[string]any{"job": s.jobViewOf(r.Context(), o)})
	case domain.RoleStaff:
		writeJSON(w, http.StatusOK, o)
	default:
		o.AssignedDriverID = "" // a customer is not sent a driver's account id
		writeJSON(w, http.StatusOK, o)
	}
}

// dispatchReady answers 503 and reports false while offers are not set up on this server.
func (s *Server) dispatchReady(w http.ResponseWriter) bool {
	if s.Dispatch == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatch is not set up on this server")
		return false
	}
	return true
}

func writeStoreError(w http.ResponseWriter, what string, err error) {
	log.Printf("driver %s: %v", what, err)
	writeError(w, http.StatusInternalServerError, "Failed to complete this request due to an internal server error")
}

// DriverOffer answers the one order the driver is being offered, or none: {"offer": {...}} or {"offer": null}.
// It is read by polling: the driver's screen asks every few seconds.
func (s *Server) DriverOffer(w http.ResponseWriter, r *http.Request) {
	if !s.dispatchReady(w) {
		return
	}
	noStore(w)
	me := principalFrom(r).User.ID
	offer, ok, err := s.Dispatch.OpenOffer(r.Context(), me)
	if err != nil {
		writeStoreError(w, "reading the offer", err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"offer": nil})
		return
	}
	o, err := s.DB.GetOrderbyId(r.Context(), offer.OrderID)
	if err != nil {
		writeStoreError(w, "reading the offered order", err)
		return
	}
	v := offerView{ID: offer.ID, RemainingSeconds: offer.RemainingSeconds, Restaurant: s.placeOf(r.Context(), o.RestaurantID)}
	if s.Drivers != nil && v.Restaurant.Latitude != nil && v.Restaurant.Longitude != nil {
		if d, err := s.Drivers.GetDriver(r.Context(), me); err == nil && d.Located() {
			km := math.Round(domain.DistanceKm(*d.Latitude, *d.Longitude, *v.Restaurant.Latitude, *v.Restaurant.Longitude)*10) / 10
			v.PickupKm = &km
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"offer": v})
}

// offerID reads the {id} of an offer. One that is not a number is an offer that is not there.
func offerID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	return id, err == nil && id > 0
}

// AcceptDriverOffer is the driver taking the order: it becomes their delivery, and they are answered with it.
func (s *Server) AcceptDriverOffer(w http.ResponseWriter, r *http.Request) {
	if !s.dispatchReady(w) {
		return
	}
	id, ok := offerID(r)
	if !ok {
		writeError(w, http.StatusNotFound, errOfferGoneMessage)
		return
	}
	o, err := s.Dispatch.AcceptOffer(r.Context(), id, principalFrom(r).User.ID)
	switch {
	case err == nil:
		noStore(w)
		writeJSON(w, http.StatusOK, map[string]any{"job": s.jobViewOf(r.Context(), o)})
	case errors.Is(err, ports.ErrOfferGone):
		writeError(w, http.StatusNotFound, errOfferGoneMessage)
	case errors.Is(err, ports.ErrDriverBusy):
		writeError(w, http.StatusConflict, "finish the delivery you have before taking another")
	default:
		writeStoreError(w, "accepting the offer", err)
	}
}

// DeclineDriverOffer is the driver saying no. The order is offered to someone else at the next round.
func (s *Server) DeclineDriverOffer(w http.ResponseWriter, r *http.Request) {
	if !s.dispatchReady(w) {
		return
	}
	id, ok := offerID(r)
	if !ok {
		writeError(w, http.StatusNotFound, errOfferGoneMessage)
		return
	}
	switch err := s.Dispatch.DeclineOffer(r.Context(), id, principalFrom(r).User.ID); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ports.ErrOfferGone):
		writeError(w, http.StatusNotFound, errOfferGoneMessage)
	default:
		writeStoreError(w, "declining the offer", err)
	}
}

// DriverJob answers the delivery the driver is doing, or none: {"job": {...}} or {"job": null}.
func (s *Server) DriverJob(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "deliveries are not set up on this server")
		return
	}
	noStore(w)
	o, ok, err := s.Jobs.CurrentJob(r.Context(), principalFrom(r).User.ID)
	switch {
	case err != nil:
		writeStoreError(w, "reading the delivery", err)
	case !ok:
		writeJSON(w, http.StatusOK, map[string]any{"job": nil})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"job": s.jobViewOf(r.Context(), o)})
	}
}
