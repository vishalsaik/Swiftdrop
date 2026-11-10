package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Restaurants and menus. Anyone signed in reads them (a customer sees only what is open and
// available); only staff change them. A customer orders menu items by id and the server prices
// them from here (see internal/catalog), so these prices are the only prices there are.

func (s *Server) isStaff(r *http.Request) bool { return principalFrom(r).User.Role == domain.RoleStaff }

func catalogError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, ports.ErrRestaurantNotFound):
		writeError(w, http.StatusNotFound, "there is no such restaurant")
	case errors.Is(err, ports.ErrMenuItemNotFound):
		writeError(w, http.StatusNotFound, "there is no such menu item")
	default:
		log.Printf("database error %s: %v", what, err)
		writeError(w, http.StatusInternalServerError, "Failed to "+what+" due to an internal server error")
	}
}

func writeFieldProblems(w http.ResponseWriter, problems map[string]string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "some fields are not acceptable", "fields": problems})
}

// nameProblem is what is wrong with a name or "" if nothing: trimmed, 1 to 80 characters, no control characters.
func nameProblem(name string) string {
	switch {
	case name == "":
		return "is required"
	case utf8.RuneCountInString(name) > domain.MaxNameLength:
		return "must be at most 80 characters"
	case hasControl(name):
		return "must not contain control characters"
	}
	return ""
}

func descriptionProblem(d string) string {
	switch {
	case utf8.RuneCountInString(d) > domain.MaxDescriptionLength:
		return "must be at most 300 characters"
	case hasControl(d):
		return "must not contain control characters"
	}
	return ""
}

func priceProblem(cents int64) string {
	if cents < 0 || cents > domain.MaxMenuPriceCents {
		return "must be between 0 and 1000000 (that is $10,000.00)"
	}
	return ""
}

// ListRestaurants answers {"restaurants": [...]}: the open ones, or for staff with ?all=true the closed ones too.
func (s *Server) ListRestaurants(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	includeClosed := s.isStaff(r) && r.URL.Query().Get("all") == "true"
	list, err := s.DB.ListRestaurants(r.Context(), includeClosed)
	if err != nil {
		catalogError(w, "list restaurants", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restaurants": list})
}

// ListMenu answers {"restaurant": ..., "items": [...]}. A customer sees an open restaurant's available
// items (a closed restaurant is not found, like one that does not exist); staff with ?all=true see everything.
func (s *Server) ListMenu(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	restaurant, err := s.DB.GetRestaurant(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		catalogError(w, "read the menu", err)
		return
	}
	staff := s.isStaff(r)
	if !staff && !restaurant.Active {
		writeError(w, http.StatusNotFound, "there is no such restaurant")
		return
	}
	all := staff && r.URL.Query().Get("all") == "true"
	items, err := s.DB.ListMenuItems(r.Context(), restaurant.ID, all)
	if err != nil {
		catalogError(w, "read the menu", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restaurant": restaurant, "items": items})
}

// CreateRestaurant opens a new restaurant: {"name": "..."}. Staff only.
func (s *Server) CreateRestaurant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if why := nameProblem(name); why != "" {
		writeFieldProblems(w, map[string]string{"name": why})
		return
	}
	created, err := s.DB.CreateRestaurant(r.Context(), domain.Restaurant{ID: uuid.NewString(), Name: name, Active: true})
	if err != nil {
		catalogError(w, "create the restaurant", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"restaurant": created})
}

// UpdateRestaurant renames a restaurant or opens or closes it: {"name"?, "active"?}. Staff only.
func (s *Server) UpdateRestaurant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   *string `json:"name"`
		Active *bool   `json:"active"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Name == nil && in.Active == nil {
		writeFieldProblems(w, map[string]string{"name": "or active: there is nothing to change"})
		return
	}
	if in.Name != nil {
		trimmed := strings.TrimSpace(*in.Name)
		if why := nameProblem(trimmed); why != "" {
			writeFieldProblems(w, map[string]string{"name": why})
			return
		}
		in.Name = &trimmed
	}
	updated, err := s.DB.UpdateRestaurant(r.Context(), mux.Vars(r)["id"], in.Name, in.Active)
	if err != nil {
		catalogError(w, "update the restaurant", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restaurant": updated})
}

// CreateMenuItem adds an item to a restaurant's menu: {"name", "description"?, "priceCents", "veg"?}. Staff only.
func (s *Server) CreateMenuItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		PriceCents  *int64 `json:"priceCents"`
		Veg         bool   `json:"veg"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	name, description := strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	problems := map[string]string{}
	if why := nameProblem(name); why != "" {
		problems["name"] = why
	}
	if why := descriptionProblem(description); why != "" {
		problems["description"] = why
	}
	switch {
	case in.PriceCents == nil:
		problems["priceCents"] = "is required, in cents"
	default:
		if why := priceProblem(*in.PriceCents); why != "" {
			problems["priceCents"] = why
		}
	}
	if len(problems) > 0 {
		writeFieldProblems(w, problems)
		return
	}
	created, err := s.DB.CreateMenuItem(r.Context(), domain.MenuItem{
		ID: uuid.NewString(), RestaurantID: mux.Vars(r)["id"], Name: name, Description: description, PriceCents: *in.PriceCents, Available: true, Veg: in.Veg,
	})
	if err != nil {
		catalogError(w, "add the menu item", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": created})
}

// UpdateMenuItem changes a menu item: any of {"name", "description", "priceCents", "available", "veg"}. Staff only.
// A new price applies to orders placed from then on; orders already placed keep theirs.
func (s *Server) UpdateMenuItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		PriceCents  *int64  `json:"priceCents"`
		Available   *bool   `json:"available"`
		Veg         *bool   `json:"veg"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Name == nil && in.Description == nil && in.PriceCents == nil && in.Available == nil && in.Veg == nil {
		writeFieldProblems(w, map[string]string{"name": "or description, priceCents, available or veg: there is nothing to change"})
		return
	}
	problems := map[string]string{}
	if in.Name != nil {
		trimmed := strings.TrimSpace(*in.Name)
		if why := nameProblem(trimmed); why != "" {
			problems["name"] = why
		}
		in.Name = &trimmed
	}
	if in.Description != nil {
		trimmed := strings.TrimSpace(*in.Description)
		if why := descriptionProblem(trimmed); why != "" {
			problems["description"] = why
		}
		in.Description = &trimmed
	}
	if in.PriceCents != nil {
		if why := priceProblem(*in.PriceCents); why != "" {
			problems["priceCents"] = why
		}
	}
	if len(problems) > 0 {
		writeFieldProblems(w, problems)
		return
	}
	updated, err := s.DB.UpdateMenuItem(r.Context(), mux.Vars(r)["id"], ports.MenuItemPatch{
		Name: in.Name, Description: in.Description, PriceCents: in.PriceCents, Available: in.Available, Veg: in.Veg,
	})
	if err != nil {
		catalogError(w, "update the menu item", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": updated})
}
