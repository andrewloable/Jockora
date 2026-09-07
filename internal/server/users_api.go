// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/store"
)

// MaxUserNameLength bounds the login name. Long enough for any real name,
// short enough that a list stays readable and a name cannot be a payload.
const MaxUserNameLength = 64

// MinUserPasswordLength matches what the bootstrap command asks for, so an
// account made in the console is no weaker than one made from the terminal.
const MinUserPasswordLength = 8

// serveUsers is the ONLY door through which an account comes to exist.
//
// There is no registration route anywhere in this server, by design: accounts
// are admin-created, and a self-service one would make the dial public the
// moment somebody found the port.
func (s *Server) serveUsers(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.TrimPrefix(path, "/admin/users")

	switch {
	case rest == "" || rest == "/":
		switch r.Method {
		case http.MethodGet:
			s.listUsers(w, r)
		case http.MethodPost:
			s.createUser(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	id, action, ok := userTarget(rest)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch {
	case r.Method == http.MethodDelete && action == "":
		s.deleteUser(w, r, id)
	case r.Method == http.MethodPost && action == "disable":
		s.setUserEnabled(w, r, id, false)
	case r.Method == http.MethodPost && action == "enable":
		s.setUserEnabled(w, r, id, true)
	case r.Method == http.MethodPost && action == "password":
		s.resetPassword(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// userTarget parses "/{id}" and "/{id}/{action}".
func userTarget(rest string) (int64, string, bool) {
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) == 0 || len(parts) > 2 {
		return 0, "", false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	if len(parts) == 1 {
		return id, "", true
	}
	return id, parts[1], true
}

// userView is an account as the console sees it. THE HASH IS NOT IN IT: it is
// not in the struct, so no future field ordering or logging can leak it.
type userView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
}

func viewOf(u store.User) userView {
	return userView{ID: u.ID, Name: u.Name, Role: u.Role, Disabled: u.Disabled}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.users.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, viewOf(u))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if field, msg := validateAccount(body.Name, body.Password, body.Role); field != "" {
		s.writeFieldError(w, field, msg)
		return
	}

	hash, err := auth.HashPassword(body.Password, auth.DefaultCost)
	if err != nil {
		// bcrypt refuses over 72 bytes. Named as a field error rather than a
		// 500, because it is the operator's input that has to change.
		s.writeFieldError(w, "password", err.Error())
		return
	}
	id, err := s.users.CreateUser(r.Context(), body.Name, hash, body.Role)
	if errors.Is(err, store.ErrUserExists) {
		http.Error(w, "that name is taken", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, id int64) {
	err := s.users.DeleteUser(r.Context(), id)
	s.writeUserResult(w, err)
}

func (s *Server) setUserEnabled(w http.ResponseWriter, r *http.Request, id int64, enabled bool) {
	// SELF-DISABLE IS REFUSED. An operator who switches their own account off
	// is locked out of the only place that could switch it back on, and the
	// last-admin rule would not catch it while another admin exists.
	if !enabled {
		if sess, _, err := s.session(r); err == nil && sess.UserID == id {
			http.Error(w, "you cannot disable your own account", http.StatusConflict)
			return
		}
	}

	var err error
	if enabled {
		err = s.users.EnableUser(r.Context(), id)
	} else {
		err = s.users.DisableUser(r.Context(), id)
	}
	s.writeUserResult(w, err)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.Password) < MinUserPasswordLength {
		s.writeFieldError(w, "password",
			"a password must be at least "+strconv.Itoa(MinUserPasswordLength)+" characters")
		return
	}
	hash, err := auth.HashPassword(body.Password, auth.DefaultCost)
	if err != nil {
		s.writeFieldError(w, "password", err.Error())
		return
	}
	s.writeUserResult(w, s.users.SetPassword(r.Context(), id, hash))
}

// writeUserResult turns the store's refusals into answers a console can explain.
func (s *Server) writeUserResult(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrLastAdmin):
		// 409, not 403: the request was allowed, the STATE refuses it. A
		// console can say "promote someone first" to one and not the other.
		http.Error(w, "that is the last enabled operator account", http.StatusConflict)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "no such account", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// validateAccount checks what the database cannot say politely.
func validateAccount(name, password, role string) (field, msg string) {
	switch {
	case strings.TrimSpace(name) == "":
		return "name", "a name is required"
	case len(name) > MaxUserNameLength:
		return "name", "a name may be at most " + strconv.Itoa(MaxUserNameLength) + " characters"
	case len(password) < MinUserPasswordLength:
		return "password", "a password must be at least " +
			strconv.Itoa(MinUserPasswordLength) + " characters"
	case role != auth.RoleAdmin && role != auth.RoleListener:
		return "role", "a role is either " + auth.RoleAdmin + " or " + auth.RoleListener
	}
	return "", ""
}

// decode reads a JSON body, answering 400 itself when it cannot.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(into); err != nil {
		http.Error(w, "expected a JSON object", http.StatusBadRequest)
		return false
	}
	return true
}

// writeFieldError names the field the console should highlight, rather than
// leaving an operator to guess which box was wrong.
func (s *Server) writeFieldError(w http.ResponseWriter, field, msg string) {
	s.writeJSON(w, http.StatusBadRequest, map[string]any{"field": field, "error": msg})
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Warn("writing a JSON response", "err", err)
	}
}
