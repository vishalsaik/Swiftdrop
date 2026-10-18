package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
	"swiftdrop/internal/order"
	"swiftdrop/internal/payment"
	"swiftdrop/internal/validation"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Server holds everything a handler might need. Handlers are methods on
// it so they all share the same DB pool and router without any globals.
type Server struct {
	Router *mux.Router
	// DB is all of storage: orders and payments. The Postgres adapter implements
	// both, which is what lets a payment and its order change in one transaction.
	DB      ports.Store
	Payment *payment.Router
}

// maxBodyBytes caps every request body.
const maxBodyBytes = 1 << 20 // 1 MB

// allowedOrigins is the CORS allow-list. Only these origins may read our
// responses in a browser. Extend this list when deploying to a real domain.
var allowedOrigins = []string{
	"http://localhost:3000",
	"http://localhost:5173",
}

// Handler returns the root http.Handler for the server: CORS wraps the whole
// router so that even 413/415/422 error responses carry the CORS headers, and
// so that OPTIONS preflights reach the CORS link before mux can 405 them.
func (s *Server) Handler() http.Handler {
	return CORS(allowedOrigins)(s.Router)
}

// NewServer builds the router and wires every route to its handler.
func NewServer(db ports.Store, pay *payment.Router) *Server {
	s := &Server{
		Router:  mux.NewRouter(),
		DB:      db,
		Payment: pay,
	}
	// The middleware chain: links run in this order for every route.
	s.Router.Use(MaxBody(maxBodyBytes))
	s.Router.Use(RequireJSON)
	s.routes()
	return s
}

func (s *Server) routes() {
	s.Router.HandleFunc("/health", GetHealth).Methods("GET")
	s.Router.HandleFunc("/orders", s.CreateOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}", s.GetOrderByID).Methods("GET")
	s.Router.HandleFunc("/orders/{id}/cancel", s.CancelOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/ready", s.ReadyState).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/deliver", s.DeliverOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/pickup", s.PickupOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/accept", s.AcceptOrder).Methods("POST")
	s.Router.HandleFunc("/orders/{id}/pay", s.PayOrder).Methods("POST")
}

func GetHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write([]byte("OK"))
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes {"error": msg} with the given status code.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// loadOrder reads the {id} route parameter and fetches that order. If it
// can't, it has already written the 400/404/500 response and returns
// ok=false, so the caller only has to return.
func (s *Server) loadOrder(w http.ResponseWriter, r *http.Request) (domain.Order, bool) {
	orderID := mux.Vars(r)["id"]
	if orderID == "" {
		writeError(w, http.StatusBadRequest, "Missing order ID route parameter")
		return domain.Order{}, false
	}
	o, err := s.DB.GetOrderbyId(r.Context(), orderID)
	if err != nil {
		log.Printf("database query error for %s: %v", orderID, err)
		if errors.Is(err, ports.ErrOrderNotFound) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("Order %s could not be found", orderID))
			return domain.Order{}, false
		}
		writeError(w, http.StatusInternalServerError, "Internal database verification step failed")
		return domain.Order{}, false
	}
	return o, true
}

// transition moves an order to `to` if the state machine allows it from
// its current state (409 otherwise). It covers every endpoint that is a
// pure change of OrderState. Endpoints that also check other facts (e.g.
// food ready, driver assigned) make their own checks and call saveState.
func (s *Server) transition(w http.ResponseWriter, r *http.Request, to domain.OrderState) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if !order.CanTransition(o.State, to) {
		writeIllegalTransition(w, o, to)
		return
	}
	s.saveState(w, r, o.ID, o.State, to)
}

// writeIllegalTransition writes the 409 for a move the state machine forbids.
func writeIllegalTransition(w http.ResponseWriter, o domain.Order, to domain.OrderState) {
	writeError(w, http.StatusConflict, fmt.Sprintf("order %s cannot be %s from state %s", o.ID, to, o.State))
}

// saveState persists the move from `from` to `to` and writes the updated
// order. The caller must already have checked that the move is allowed, and
// `from` is the state it read: the write only happens if the order is still in
// it, so a request that lost a race gets a 409 instead of overwriting the
// winner.
func (s *Server) saveState(w http.ResponseWriter, r *http.Request, id string, from, to domain.OrderState) {
	updated, err := s.DB.UpdateOrderState(r.Context(), id, string(from), string(to))
	if err != nil {
		log.Printf("database update error for %s: %v", id, err)
		writeWriteError(w, id, err, "Failed to update order due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// writeWriteError answers a failed state write. A lost race is the caller's to
// retry (409, with the read-then-write it was part of now out of date), an
// order that vanished in between is a 404, and anything else is a 500 with the
// caller's generic message (the real cause is already in the log).
func writeWriteError(w http.ResponseWriter, id string, err error, internalMsg string) {
	switch {
	case errors.Is(err, ports.ErrStateConflict):
		writeError(w, http.StatusConflict, fmt.Sprintf("order %s changed while this request was running, read it again", id))
	case errors.Is(err, ports.ErrOrderNotFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("Order %s could not be found", id))
	default:
		writeError(w, http.StatusInternalServerError, internalMsg)
	}
}

func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	// Decode strictly: a field we don't know about is a client mistake worth
	// telling them about, not something to silently drop.
	var orderItem domain.Order
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&orderItem); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// The validator chain: first failing rule wins.
	if err := validation.Order(orderItem); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// A new order always starts as "created"; the client doesn't choose its state.
	orderItem.State = domain.OrderCreated

	savedOrder, err := s.DB.CreateOrder(r.Context(), orderItem)
	if err != nil {
		if errors.Is(err, ports.ErrOrderExists) {
			writeError(w, http.StatusConflict, fmt.Sprintf("order %s already exists", orderItem.ID))
			return
		}
		log.Printf("database insertion error: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to create order due to an internal server error")
		return
	}

	// Respond with the stored row (not the request), so the client sees what
	// the database actually holds.
	writeJSON(w, http.StatusCreated, savedOrder)
}

func (s *Server) GetOrderByID(w http.ResponseWriter, r *http.Request) {
	if o, ok := s.loadOrder(w, r); ok {
		writeJSON(w, http.StatusOK, o)
	}
}

func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, domain.OrderCancelled)
}

func (s *Server) DeliverOrder(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, domain.OrderDelivered)
}

// ReadyState marks the food ready. It doesn't change OrderState (food
// readiness is an independent fact), so it can't use transition.
func (s *Server) ReadyState(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if o.State != domain.OrderPreparing {
		writeError(w, http.StatusConflict, fmt.Sprintf("order %s cannot be marked ready from state %s", o.ID, o.State))
		return
	}
	updated, err := s.DB.SetFoodReady(r.Context(), o.ID)
	if err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to set food ready due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) PickupOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	// 1. the state machine must allow preparing -> picked_up
	if !order.CanTransition(o.State, domain.OrderPickedUp) {
		writeIllegalTransition(w, o, domain.OrderPickedUp)
		return
	}
	// 2. the kitchen has marked the food ready
	if o.FoodReady == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("Food is not ready yet for %s", o.ID))
		return
	}
	// 3. a driver has been assigned
	if o.AssignedDriverID == "" {
		writeError(w, http.StatusConflict, fmt.Sprintf("No driver was assigned to this order %s", o.ID))
		return
	}
	s.saveState(w, r, o.ID, o.State, domain.OrderPickedUp)
}
func (s *Server) AcceptOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	if !order.CanTransition(o.State, domain.OrderPreparing) {
		writeIllegalTransition(w, o, domain.OrderPreparing)
		return
	}
	// TODO: stand-in for real dispatch. Replace once the driver/dispatch feature exists.
	driverID := uuid.New().String()
	updated, err := s.DB.AcceptOrder(r.Context(), o.ID, driverID, string(o.State), string(domain.OrderPreparing))
	if err != nil {
		log.Printf("database update error for %s: %v", o.ID, err)
		writeWriteError(w, o.ID, err, "Failed to accept order due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// paymentLease is how long a request that has claimed a payment may work on it
// before a later request is allowed to take it over. It must be longer than the
// slowest legitimate provider call (the Stripe client gives up after 30s), or a
// second request could take over a payment that is still being processed.
const paymentLease = 60 * time.Second

// PayOrder pays for an order, and is idempotent: calling it again for the same
// order never charges twice. The payment record decides what a request is.
//
//	no payment for the order  -> a NEW payment: claim it, then authorize, then record the result
//	payment authorized        -> a retry: replay the success
//	payment declined          -> a retry: replay the decline
//	payment processing        -> a retry: 409 while its lease is live; once the
//	                             lease has expired, take it over and ask the
//	                             provider again with the SAME payment id
//
// The claim comes BEFORE the provider call. It is what stops two simultaneous
// requests from both charging: the database lets exactly one of them in.
func (s *Server) PayOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	p, err := s.DB.GetPaymentByOrder(r.Context(), o.ID)
	switch {
	case err == nil:
		s.continuePayment(w, r, o, p)
	case errors.Is(err, ports.ErrPaymentNotFound):
		s.startPayment(w, r, o)
	default:
		log.Printf("database query error for the payment of %s: %v", o.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to look up the payment due to an internal server error")
	}
}

// providerFor picks the payment provider (Strategy, via the Factory) for the
// order's payment type. If it cannot, it has already answered the request.
func (s *Server) providerFor(w http.ResponseWriter, o domain.Order) (payment.PaymentProvider, bool) {
	provider, err := s.Payment.For(o.PaymentType)
	if err != nil {
		// Our stored data, not this request, named a type we can't handle
		// (e.g. an order created before the payment_type column existed), so
		// this is a 500, and the real cause goes to the log, not the client.
		log.Printf("no payment provider for order %s (payment type %q): %v", o.ID, o.PaymentType, err)
		writeError(w, http.StatusInternalServerError, "Payment is not available for this order")
		return nil, false
	}
	return provider, true
}

// startPayment handles an order that has no payment yet.
func (s *Server) startPayment(w http.ResponseWriter, r *http.Request, o domain.Order) {
	if !order.CanTransition(o.State, domain.OrderPaymentPending) {
		writeIllegalTransition(w, o, domain.OrderPaid)
		return
	}
	// The provider lookup can fail, so it runs before anything is saved: a
	// failure leaves the order untouched (still "created"), not stranded in
	// payment_pending with a payment nobody will finish.
	provider, ok := s.providerFor(w, o)
	if !ok {
		return
	}
	// The claim. The payment row and the order's move to payment_pending are
	// written in one transaction, and only if the order has no payment yet and
	// is still in the state we read. Of any number of simultaneous requests,
	// exactly one gets claimed = true; the rest never reach the provider.
	claim := domain.Payment{ID: uuid.New().String(), OrderID: o.ID, AmountCents: o.TotalCents()}
	p, claimed, err := s.DB.StartPayment(r.Context(), claim, paymentLease, string(o.State), string(domain.OrderPaymentPending))
	if err != nil {
		log.Printf("database error starting the payment of %s: %v", o.ID, err)
		writeWriteError(w, o.ID, err, "Failed to start payment due to an internal server error")
		return
	}
	if !claimed {
		// Another request claimed it between our lookup and our claim, so this
		// one is a retry after all.
		s.continuePayment(w, r, o, p)
		return
	}
	s.authorize(w, r, p, provider)
}

// continuePayment handles an order that already has a payment, so this request
// is a retry.
func (s *Server) continuePayment(w http.ResponseWriter, r *http.Request, o domain.Order, p domain.Payment) {
	switch p.Status {
	case domain.PaymentAuthorized, domain.PaymentDeclined:
		s.replayPayment(w, r, p)
	case domain.PaymentProcessing:
		s.takeOverPayment(w, r, o, p)
	default:
		log.Printf("payment %s for order %s has an unknown status %q", p.ID, p.OrderID, p.Status)
		writeError(w, http.StatusInternalServerError, "Failed to read the payment due to an internal server error")
	}
}

// replayPayment answers a retry of a finished payment with the original
// outcome, without calling the provider again.
func (s *Server) replayPayment(w http.ResponseWriter, r *http.Request, p domain.Payment) {
	switch p.Status {
	case domain.PaymentAuthorized:
		current, err := s.DB.GetOrderbyId(r.Context(), p.OrderID)
		if err != nil {
			log.Printf("database query error for %s: %v", p.OrderID, err)
			writeWriteError(w, p.OrderID, err, "Internal database verification step failed")
			return
		}
		writeJSON(w, http.StatusOK, current)
	case domain.PaymentDeclined:
		writeError(w, http.StatusPaymentRequired, declineReason(p.Reason))
	default:
		log.Printf("payment %s for order %s cannot be replayed from status %q", p.ID, p.OrderID, p.Status)
		writeError(w, http.StatusInternalServerError, "Failed to read the payment due to an internal server error")
	}
}

// takeOverPayment handles a retry of a payment that is still processing. If
// its lease is live, someone is working on it: say so. If the lease has run
// out, its owner probably died, so take it over and ask the provider again with
// the SAME payment id (the provider returns the saved answer, or runs it once),
// never with a new one.
func (s *Server) takeOverPayment(w http.ResponseWriter, r *http.Request, o domain.Order, p domain.Payment) {
	taken, ok, err := s.DB.TakeOverPayment(r.Context(), p.ID, paymentLease)
	if err != nil {
		log.Printf("database error taking over payment %s: %v", p.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to read the payment due to an internal server error")
		return
	}
	if !ok {
		// A live lease, or it finished while we were looking. Read it again.
		latest, err := s.DB.GetPaymentByOrder(r.Context(), o.ID)
		if err != nil {
			log.Printf("database query error for the payment of %s: %v", o.ID, err)
			writeError(w, http.StatusInternalServerError, "Failed to read the payment due to an internal server error")
			return
		}
		if latest.Status == domain.PaymentProcessing {
			writeError(w, http.StatusConflict, fmt.Sprintf("the payment for order %s is still being processed, try again shortly", o.ID))
			return
		}
		s.replayPayment(w, r, latest)
		return
	}
	provider, ok := s.providerFor(w, o)
	if !ok {
		return
	}
	s.authorize(w, r, taken, provider)
}

// authorize asks the provider and records the answer. p is a payment this
// request holds the claim on. The payment's own id is the provider's
// idempotency key, and the amount is the one stored when it was claimed, so a
// retry or a takeover asks the provider the very same question.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, p domain.Payment, provider payment.PaymentProvider) {
	var body struct {
		PaymentMethodID string `json:"paymentMethodId"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	req := domain.PaymentRequest{
		OrderID:         p.OrderID,
		IdempotencyKey:  p.ID,
		AmountCents:     p.AmountCents,
		Currency:        "USD",
		PaymentMethodID: body.PaymentMethodID,
	}
	result, err := provider.Authorize(r.Context(), req)
	if err != nil {
		// We do not know what happened. The payment stays processing: once its
		// lease runs out, a retry asks the provider again with the same id.
		log.Printf("payment authorisation error for %s: %v", p.OrderID, err)
		writeError(w, http.StatusInternalServerError, "Payment authorisation failed")
		return
	}
	// A provider that returns neither a result nor an error broke its contract.
	// We don't know what happened, so treat it like an error, not a decline.
	if result == nil {
		log.Printf("payment provider returned no result and no error for %s", p.OrderID)
		writeError(w, http.StatusInternalServerError, "Payment authorisation failed")
		return
	}
	switch result.Status {
	case domain.PaymentAuthorized:
		s.finishPayment(w, r, p, domain.PaymentAuthorized, result.PaymentID, "", domain.OrderPaid)
	case domain.PaymentDeclined:
		// A decline is a normal answer, not an error: the order ends in a final
		// state (cancelled) and the client gets the reason.
		s.finishPayment(w, r, p, domain.PaymentDeclined, result.PaymentID, declineReason(result.Reason), domain.OrderCancelled)
	default:
		log.Printf("payment provider answered %q for %s, which is neither authorized nor declined", result.Status, p.OrderID)
		writeError(w, http.StatusInternalServerError, "Payment authorisation failed")
	}
}

// finishPayment records the outcome and moves the order, in one transaction,
// and answers the request.
func (s *Server) finishPayment(w http.ResponseWriter, r *http.Request, p domain.Payment, status domain.PaymentStatus, providerPaymentID, reason string, to domain.OrderState) {
	updated, err := s.DB.FinishPayment(r.Context(), p.ID, status, providerPaymentID, reason,
		string(domain.OrderPaymentPending), string(to))
	switch {
	case err == nil:
		if status == domain.PaymentDeclined {
			writeError(w, http.StatusPaymentRequired, reason)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	case errors.Is(err, ports.ErrPaymentNotProcessing):
		// Another request recorded the outcome first (for example a retry that
		// took the payment over). Answer with theirs.
		latest, getErr := s.DB.GetPaymentByOrder(r.Context(), p.OrderID)
		if getErr != nil {
			log.Printf("database query error for the payment of %s: %v", p.OrderID, getErr)
			writeError(w, http.StatusInternalServerError, "Failed to read the payment due to an internal server error")
			return
		}
		s.replayPayment(w, r, latest)
	case errors.Is(err, ports.ErrStateConflict):
		// The provider has answered but the order moved on while it was
		// working (for example it was cancelled). Nothing was recorded, so the
		// payment stays processing. This needs a human or a refund, so say so.
		log.Printf("NEEDS REVIEW: payment %s for order %s was answered %q but the order is no longer %s: %v",
			p.ID, p.OrderID, status, domain.OrderPaymentPending, err)
		writeWriteError(w, p.OrderID, err, "Failed to record the payment due to an internal server error")
	default:
		log.Printf("database error recording payment %s for %s: %v", p.ID, p.OrderID, err)
		writeWriteError(w, p.OrderID, err, "Failed to record the payment due to an internal server error")
	}
}

// declineReason is what the client is told when a payment was declined and the
// provider gave no reason.
func declineReason(reason string) string {
	if reason == "" {
		return "payment declined"
	}
	return reason
}
