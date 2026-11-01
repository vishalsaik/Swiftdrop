package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"swiftdrop/internal/databases/ports"

	"github.com/gorilla/mux"
)

// alertTimeout bounds writing an alert on a context that outlives the request: an
// alert is raised when something has already gone wrong, and a client that hangs
// up must not be the reason it is lost.
var alertTimeout = 5 * time.Second

// raiseAlert makes a problem visible: it is logged, as it always was, and kept as
// an alert a person can see on the dashboard and close. Failing to keep it is
// itself logged, since the log line is then the only record.
func (s *Server) raiseAlert(ctx context.Context, kind, subject, orderID, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	log.Printf("NEEDS REVIEW: %s", message)
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertTimeout)
	defer cancel()
	if err := s.DB.RaiseAlert(actx, kind, subject, orderID, message); err != nil {
		log.Printf("could not keep the alert (%s, %s): %v", kind, subject, err)
	}
}

// ListAlerts answers the problems a person has to look at: the open ones, or with
// ?all=true the resolved ones too.
func (s *Server) ListAlerts(w http.ResponseWriter, r *http.Request) {
	openOnly := r.URL.Query().Get("all") != "true"
	alerts, err := s.DB.ListAlerts(r.Context(), openOnly, maxListLimit)
	if err != nil {
		log.Printf("database query error listing alerts: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to list alerts due to an internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

// ResolveAlert closes an alert with an optional note ({"note": "..."}).
func (s *Server) ResolveAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "alert id must be a positive whole number")
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	alert, err := s.DB.ResolveAlert(r.Context(), id, body.Note)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, alert)
	case errors.Is(err, ports.ErrAlertNotFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("alert %d could not be found", id))
	case errors.Is(err, ports.ErrAlertResolved):
		writeError(w, http.StatusConflict, fmt.Sprintf("alert %d is already resolved", id))
	default:
		log.Printf("database error resolving alert %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "Failed to resolve the alert due to an internal server error")
	}
}
