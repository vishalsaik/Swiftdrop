package handlers

import (
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"
)

// The driver's own routes: whether they are taking deliveries, and where they are. Only a driver account
// reaches them (driverOnly), and a driver only ever reads and changes themselves.

// driversReady answers 503 and reports false while drivers are not set up on this server.
func (s *Server) driversReady(w http.ResponseWriter) bool {
	if s.Drivers == nil {
		writeError(w, http.StatusServiceUnavailable, "drivers are not set up on this server")
		return false
	}
	return true
}

// writeDriver answers a driver's state, or what went wrong reading or changing it.
func writeDriver(w http.ResponseWriter, d domain.Driver, err error) {
	switch {
	case err == nil:
		noStore(w)
		writeJSON(w, http.StatusOK, map[string]any{"driver": d})
	case errors.Is(err, ports.ErrDriverNotFound):
		writeError(w, http.StatusNotFound, "there is no such driver account")
	case errors.Is(err, ports.ErrDriverOffline):
		writeError(w, http.StatusConflict, "go online before sending your location")
	default:
		log.Printf("driver store error: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to complete this request due to an internal server error")
	}
}

// DriverMe answers the signed-in driver's own state: {"driver": {...}}.
func (s *Server) DriverMe(w http.ResponseWriter, r *http.Request) {
	if !s.driversReady(w) {
		return
	}
	d, err := s.Drivers.GetDriver(r.Context(), principalFrom(r).User.ID)
	writeDriver(w, d, err)
}

// DriverOnline and DriverOffline say whether the driver is taking deliveries. Saying what is already true
// is fine and changes nothing.
func (s *Server) DriverOnline(w http.ResponseWriter, r *http.Request)  { s.setOnline(w, r, true) }
func (s *Server) DriverOffline(w http.ResponseWriter, r *http.Request) { s.setOnline(w, r, false) }

func (s *Server) setOnline(w http.ResponseWriter, r *http.Request, online bool) {
	if !s.driversReady(w) {
		return
	}
	d, err := s.Drivers.SetOnline(r.Context(), principalFrom(r).User.ID, online)
	writeDriver(w, d, err)
}

// DriverLocation replaces the point the driver was last at. The browser asks for the position only when the
// driver presses Go online and then sends it again every 30 seconds (that is the page's business); here
// each driver may send at most a handful a minute, and only while online.
func (s *Server) DriverLocation(w http.ResponseWriter, r *http.Request) {
	if !s.driversReady(w) {
		return
	}
	me := principalFrom(r).User.ID
	if s.DriverLimit != nil {
		if ok, wait := s.DriverLimit.Allow(me); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "too many locations; wait a moment and try again")
			return
		}
	}
	var in struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	if in.Latitude == nil || in.Longitude == nil {
		writeError(w, http.StatusUnprocessableEntity, "latitude and longitude are required")
		return
	}
	if !domain.ValidPoint(*in.Latitude, *in.Longitude) {
		writeError(w, http.StatusUnprocessableEntity, "that is not a place on the map: latitude is -90 to 90 and longitude -180 to 180")
		return
	}
	d, err := s.Drivers.SetLocation(r.Context(), me, *in.Latitude, *in.Longitude)
	writeDriver(w, d, err)
}

// ListDrivers answers every driver for staff, online ones first: {"drivers": [...]}. It is where staff see
// who is online, free or busy, and when they were last heard from.
func (s *Server) ListDrivers(w http.ResponseWriter, r *http.Request) {
	if !s.driversReady(w) {
		return
	}
	noStore(w)
	drivers, err := s.Drivers.ListDrivers(r.Context())
	if err != nil {
		log.Printf("driver store error: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to list drivers due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drivers": drivers})
}
