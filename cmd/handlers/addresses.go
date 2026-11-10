package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"swiftdrop/internal/catalog"
	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Saved addresses, and working out where an order is going. An address is always its owner's: asking
// for someone else's is the same as asking for one that is not there.

// addressInput is an address as a client sends it. The coordinates are pointers so that leaving them
// out is told apart from sending 0, 0 (a real place in the sea): an address without a place cannot be
// delivered to.
type addressInput struct {
	Label      string   `json:"label"`
	Line1      string   `json:"line1"`
	Landmark   string   `json:"landmark"`
	City       string   `json:"city"`
	PostalCode string   `json:"postalCode"`
	Latitude   *float64 `json:"latitude"`
	Longitude  *float64 `json:"longitude"`
}

// address is the input made tidy, and what is wrong with it by field (nil if nothing).
func (in addressInput) address() (domain.Address, map[string]string) {
	a := domain.Address{Label: in.Label, Line1: in.Line1, Landmark: in.Landmark, City: in.City, PostalCode: in.PostalCode}.Tidy()
	if in.Latitude == nil || in.Longitude == nil {
		problems := a.Problems()
		if problems == nil {
			problems = map[string]string{}
		}
		problems["latitude"] = "and longitude are required: choose a place from the address search, or use your location"
		return a, problems
	}
	a.Latitude, a.Longitude = *in.Latitude, *in.Longitude
	return a, a.Problems()
}

// sentence turns field problems into one line for the places that answer with a single message.
func sentence(prefix string, problems map[string]string) string {
	fields := make([]string, 0, len(problems))
	for f := range problems {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f + " " + problems[f]
	}
	return prefix + strings.Join(parts, "; ")
}

func addressError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, ports.ErrAddressNotFound):
		writeError(w, http.StatusNotFound, "there is no such saved address")
	case errors.Is(err, ports.ErrTooManyAddresses):
		writeError(w, http.StatusConflict, fmt.Sprintf("you can keep at most %d saved addresses; delete one first", domain.MaxSavedAddresses))
	default:
		log.Printf("database error %s: %v", what, err)
		writeError(w, http.StatusInternalServerError, "Failed to "+what+" due to an internal server error")
	}
}

// ListAddresses answers {"addresses": [...]}: the signed-in person's own, oldest first.
func (s *Server) ListAddresses(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	list, err := s.DB.ListAddresses(r.Context(), principalFrom(r).User.ID)
	if err != nil {
		addressError(w, "list your addresses", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addresses": list})
}

// CreateAddress saves an address for the signed-in person: {"line1", "latitude", "longitude", and
// optionally "label", "landmark", "city", "postalCode"}.
func (s *Server) CreateAddress(w http.ResponseWriter, r *http.Request) {
	var in addressInput
	if !decodeStrict(w, r, &in) {
		return
	}
	a, problems := in.address()
	if problems != nil {
		writeFieldProblems(w, problems)
		return
	}
	saved, err := s.DB.CreateAddress(r.Context(), domain.SavedAddress{ID: uuid.NewString(), UserID: principalFrom(r).User.ID, Address: a})
	if err != nil {
		addressError(w, "save the address", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"address": saved})
}

// UpdateAddress replaces one of the signed-in person's addresses. Orders already placed with it keep
// the address they were placed with.
func (s *Server) UpdateAddress(w http.ResponseWriter, r *http.Request) {
	var in addressInput
	if !decodeStrict(w, r, &in) {
		return
	}
	a, problems := in.address()
	if problems != nil {
		writeFieldProblems(w, problems)
		return
	}
	saved, err := s.DB.UpdateAddress(r.Context(), principalFrom(r).User.ID, mux.Vars(r)["id"], a)
	if err != nil {
		addressError(w, "update the address", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"address": saved})
}

// DeleteAddress removes one of the signed-in person's addresses (204).
func (s *Server) DeleteAddress(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.DeleteAddress(r.Context(), principalFrom(r).User.ID, mux.Vars(r)["id"]); err != nil {
		addressError(w, "delete the address", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deliveryFor works out where an order is going and whether its restaurant delivers there. The
// address is a saved one of the order's customer (deliveryAddressId) or one sent with the order
// (deliveryAddress), never both and never neither. unfit is what is wrong with the order, said for
// the person placing it; err is a failure of storage.
func (s *Server) deliveryFor(ctx context.Context, customerID, restaurantID, savedID string, inline *addressInput) (to *domain.Address, unfit string, err error) {
	var a domain.Address
	switch {
	case savedID != "" && inline != nil:
		return nil, "send either deliveryAddressId or deliveryAddress, not both", nil
	case savedID == "" && inline == nil:
		return nil, "a delivery address is required: send deliveryAddressId (a saved address) or deliveryAddress", nil
	case savedID != "":
		saved, err := s.DB.GetAddress(ctx, customerID, savedID)
		if errors.Is(err, ports.ErrAddressNotFound) {
			return nil, fmt.Sprintf("there is no saved address %q for this customer", savedID), nil
		}
		if err != nil {
			return nil, "", err
		}
		a = saved.Address
	default:
		var problems map[string]string
		if a, problems = inline.address(); problems != nil {
			return nil, sentence("deliveryAddress: ", problems), nil
		}
	}
	switch err := (catalog.Deliverer{Store: s.DB}).Check(ctx, restaurantID, a); {
	case err == nil:
		return &a, "", nil
	default:
		var p *catalog.Problem
		if errors.As(err, &p) {
			return nil, p.Message, nil
		}
		return nil, "", err
	}
}
