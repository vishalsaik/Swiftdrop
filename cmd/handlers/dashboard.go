package handlers

import (
	"log"
	"net/http"
	"strconv"

	"swiftdrop/internal/domain"
	"swiftdrop/web"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// ListOrders answers the dashboard's board: the newest orders, each with its
// payment summary and payment issue, in one read. ?limit=N asks for fewer or
// more than the default, up to a ceiling so one request cannot ask for the table.
func (s *Server) ListOrders(w http.ResponseWriter, r *http.Request) {
	limit := defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "limit must be a whole number from 1 to "+strconv.Itoa(maxListLimit))
			return
		}
		limit = n
	}
	var rows []domain.OrderWithPayment
	var err error
	if p := principalFrom(r); p.User.Role == domain.RoleStaff {
		rows, err = s.DB.ListOrders(r.Context(), limit)
	} else { // a customer's board is their own orders and nothing else
		rows, err = s.DB.ListOrdersByCustomer(r.Context(), p.User.ID, limit)
	}
	if err != nil {
		log.Printf("database query error listing orders: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to list orders due to an internal server error")
		return
	}
	views := make([]orderView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewOf(row.Order, row.Payment))
	}
	writeJSON(w, http.StatusOK, map[string][]orderView{"orders": views})
}

// dashboardRoutes serves the dashboard at /dashboard/ and sends / and
// /dashboard there. The files are compiled in, so they only change with the
// binary; no-cache lets a browser reuse them but always check first.
func (s *Server) dashboardRoutes() {
	files := http.StripPrefix("/dashboard/", http.FileServerFS(web.Static()))
	s.Router.PathPrefix("/dashboard/").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})).Methods(http.MethodGet, http.MethodHead)

	toDashboard := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	}
	s.Router.HandleFunc("/dashboard", toDashboard).Methods(http.MethodGet)
	s.Router.HandleFunc("/", toDashboard).Methods(http.MethodGet)
}
