package handlers

import (
	"log"
	"net/http"
	"strconv"

	"swiftdrop/internal/databases/ports"
	"swiftdrop/internal/domain"

	"github.com/gorilla/mux"
)

// Managing accounts, for staff. Everything here is behind staffOnly, and the auth service
// checks it again. A temporary password is in exactly two answers (an account made, a
// password reset), is never stored or logged, and those answers are never cached.

// passwordChangeAllowed are the only routes an account that must choose a new password may use.
func passwordChangeAllowed(r *http.Request) bool {
	switch r.URL.Path {
	case "/auth/me", "/auth/password", "/auth/logout":
		return true
	}
	return false
}

// ListAccounts answers accounts, newest first: {"accounts": [...]}. Filters: ?role=staff or
// customer, ?status=active or disabled, ?q= (part of the email or the name), ?limit=.
func (s *Server) ListAccounts(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	query := r.URL.Query()
	q := ports.UserQuery{Role: domain.Role(query.Get("role")), Status: query.Get("status"), Search: query.Get("q")}
	if q.Role != "" && !q.Role.Valid() {
		writeError(w, http.StatusBadRequest, "role must be staff or customer")
		return
	}
	if q.Status != "" && q.Status != "active" && q.Status != "disabled" {
		writeError(w, http.StatusBadRequest, "status must be active or disabled")
		return
	}
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive whole number")
			return
		}
		q.Limit = n
	}
	accounts, err := s.Accounts.ListAccounts(r.Context(), principalFrom(r), q)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

// GetAccount answers one account with the latest lines of its history: {"user": ..., "events": [...]}.
func (s *Server) GetAccount(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	user, events, err := s.Accounts.AccountDetail(r.Context(), principalFrom(r), mux.Vars(r)["id"])
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "events": events})
}

// CreateAccount makes an account for someone else and answers 201 with the account and its
// temporary password, shown that once.
func (s *Server) CreateAccount(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var in struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	actor := principalFrom(r)
	user, password, err := s.Accounts.CreateAccount(r.Context(), actor, in.Email, in.Name, domain.Role(in.Role))
	if err != nil {
		writeAuthError(w, err)
		return
	}
	log.Printf("account created: %s (%s) by %s", user.Email, user.Role, actor.User.Email)
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "temporaryPassword": password})
}

// DisableAccount and EnableAccount switch an account off or on; disabling ends its sign-ins.
func (s *Server) DisableAccount(w http.ResponseWriter, r *http.Request) { s.setDisabled(w, r, true) }
func (s *Server) EnableAccount(w http.ResponseWriter, r *http.Request)  { s.setDisabled(w, r, false) }

func (s *Server) setDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	noStore(w)
	actor := principalFrom(r)
	user, err := s.Accounts.SetDisabled(r.Context(), actor, mux.Vars(r)["id"], disabled)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	log.Printf("account %s: disabled=%v by %s", user.Email, disabled, actor.User.Email)
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// ResetAccountPassword gives an account a new temporary password, shown that once.
func (s *Server) ResetAccountPassword(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	actor := principalFrom(r)
	user, password, err := s.Accounts.ResetPassword(r.Context(), actor, mux.Vars(r)["id"])
	if err != nil {
		writeAuthError(w, err)
		return
	}
	log.Printf("password reset: %s by %s", user.Email, actor.User.Email)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "temporaryPassword": password})
}

// refusePasswordChangeRequired is for the guard: an account that must choose a new password gets
// this for every other route.
func refusePasswordChangeRequired(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "you must choose a new password before doing anything else",
		"code":  "password_change_required",
	})
}
