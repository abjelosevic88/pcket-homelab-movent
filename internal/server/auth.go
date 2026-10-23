package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

type ctxKey int

const (
	userKey ctxKey = iota + 1
	deviceKey
)

const sessionCookie = "pm_session"

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

func deviceFrom(ctx context.Context) *store.Device {
	d, _ := ctx.Value(deviceKey).(*store.Device)
	return d
}

var errLinkScope = errors.New("outside the link's scope")

// credential finds the key a request carries. Trackers are configured in
// many ways: Dawarich and Overland put api_key in the query, newer clients
// send a bearer token, OwnTracks uses HTTP basic auth (the key as the
// password), Traccar/OsmAnd clients send their device identifier as id.
func credential(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if t := r.Header.Get("X-API-Key"); t != "" {
		return t
	}
	if k := r.URL.Query().Get("api_key"); k != "" {
		return k
	}
	if _, pass, ok := r.BasicAuth(); ok && pass != "" {
		return pass
	}
	// Traccar/OsmAnd clients only have a device identifier: it may be the key.
	if id := r.URL.Query().Get("id"); strings.HasPrefix(id, "pm_") {
		return id
	}
	return ""
}

// resolve identifies the caller: a device key (with its scope), a personal
// API token, a Pocket Core token, or the browser session.
func (s *Server) resolve(r *http.Request) (*store.User, *store.Device, error) {
	ctx := r.Context()
	if tok := credential(r); tok != "" {
		if s.core.Configured() && core.IsLinkToken(tok) {
			u, err := s.linkUser(ctx, tok, r)
			return u, nil, err
		}
		if s.core.Configured() && core.IsCoreToken(tok) {
			u, err := s.coreUser(ctx, tok)
			return u, nil, err
		}
		if strings.HasPrefix(tok, "pt_") {
			u, err := s.store.GetUserByAPIToken(ctx, tok)
			return u, nil, err
		}
		if d, err := s.store.DeviceByKey(ctx, tok); err == nil {
			u, err := s.store.GetUser(ctx, d.UserID)
			if err != nil {
				return nil, nil, err
			}
			s.store.TouchDevice(ctx, d.ID, clientIP(r), r.UserAgent())
			return u, d, nil
		}
		// A Basic-auth username that is a device key (some apps only have a username field).
		if user, _, ok := r.BasicAuth(); ok && user != "" {
			if d, err := s.store.DeviceByKey(ctx, user); err == nil {
				u, err := s.store.GetUser(ctx, d.UserID)
				if err == nil {
					s.store.TouchDevice(ctx, d.ID, clientIP(r), r.UserAgent())
				}
				return u, d, err
			}
		}
		return nil, nil, store.ErrNotFound
	}
	switch s.cfg.AuthMode {
	case "none":
		u, err := s.firstOrSystemUser(ctx, "admin@localhost", "Admin")
		return u, nil, err
	case "proxy":
		name := r.Header.Get(s.cfg.AuthProxyHeader)
		if name == "" {
			return nil, nil, http.ErrNoCookie
		}
		email := r.Header.Get(s.cfg.AuthProxyEmailHeader)
		if email == "" {
			email = name
			if !strings.Contains(email, "@") {
				email += "@proxy"
			}
		}
		u, err := s.firstOrSystemUser(ctx, strings.ToLower(email), name)
		return u, nil, err
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, nil, err
	}
	u, err := s.sessionUser(ctx, c.Value)
	return u, nil, err
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// guard builds a middleware that admits a caller when ok(user, device) holds.
func (s *Server) guard(ok func(*store.User, *store.Device) bool, denied string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, d, err := s.resolve(r)
			if errors.Is(err, errLinkScope) {
				writeErr(w, http.StatusForbidden, "this Pocket Core link does not cover "+r.Method+" "+r.URL.Path)
				return
			}
			if err != nil || u == nil {
				writeErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if !ok(u, d) {
				writeErr(w, http.StatusForbidden, denied)
				return
			}
			ctx := context.WithValue(r.Context(), userKey, u)
			if d != nil {
				ctx = context.WithValue(ctx, deviceKey, d)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requireUser admits people (session, API token, Core) but not device keys.
func (s *Server) requireUser(next http.Handler) http.Handler {
	return s.guard(func(u *store.User, d *store.Device) bool { return d == nil }, "a device key cannot do this; sign in")(next)
}

// requireTracker admits whoever may send points.
func (s *Server) requireTracker(next http.Handler) http.Handler {
	return s.guard(func(u *store.User, d *store.Device) bool { return d == nil || d.CanTrack() }, "this key can only read; give the device the track scope")(next)
}

// requireReader admits whoever may read history. Tracker apps also read
// their own account (users/me, settings) to verify a key, so those two are
// open to track-only keys as well.
func (s *Server) requireReader(next http.Handler) http.Handler {
	return s.guard(func(u *store.User, d *store.Device) bool { return d == nil || d.CanRead() }, "this key can only send points; give it the read scope in Devices")(next)
}

func (s *Server) readerOrTracker(next http.Handler) http.Handler {
	return s.guard(func(u *store.User, d *store.Device) bool { return true }, "")(next)
}

// coreUser validates a token Pocket Core issued and maps it to the local
// account with the same identity, creating a member on first sight.
func (s *Server) coreUser(ctx context.Context, tok string) (*store.User, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, err
	}
	if !in.Active || in.User == nil || in.User.Email == "" {
		return nil, http.ErrNoCookie
	}
	return s.coreIdentityUser(ctx, in)
}

// firstOrSystemUser returns the user with that email, creating it when missing.
// The very first account becomes admin, later ones are members.
func (s *Server) firstOrSystemUser(ctx context.Context, email, name string) (*store.User, error) {
	if u, err := s.store.GetUserByEmail(ctx, email); err == nil {
		return u, nil
	}
	role := "member"
	if n, _ := s.store.CountUsers(ctx); n == 0 {
		role = "admin"
	}
	u := &store.User{Email: email, Name: name, Role: role}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	s.log.Info("auto-provisioned user", "email", email, "mode", s.cfg.AuthMode, "role", role)
	return u, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, id string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/", HttpOnly: true,
		Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds()),
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.fail(w, err, "count users")
		return
	}
	st, _ := s.store.GetSettings(r.Context(), 0)
	u, d, _ := s.resolve(r)
	if d != nil {
		u = nil // a device key is not a browser session
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup":   n == 0 && s.cfg.AuthMode == "local",
		"auth_mode":     s.cfg.AuthMode,
		"authenticated": u != nil,
		"user":          u,
		"version":       Version,
		"title":         st.Title,
		"oidc":          s.oidcEnabled() && s.cfg.AuthMode == "local",
		"oidc_name":     s.oidcName(),
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		s.fail(w, err, "count users")
		return
	}
	if n > 0 || s.cfg.AuthMode != "local" {
		writeErr(w, http.StatusForbidden, "setup already completed")
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" || len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "email and a password of at least 8 characters are required")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	u := &store.User{Email: in.Email, Name: in.Name, PasswordHash: string(hash), Role: "admin"}
	if err := s.store.CreateUser(ctx, u); err != nil {
		s.fail(w, err, "create user")
		return
	}
	st, _ := s.store.GetSettings(ctx, 0)
	st.SetupComplete = true
	_ = s.store.SaveGlobalSettings(ctx, st)
	id, err := s.store.CreateSession(ctx, u.ID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	s.setSessionCookie(w, id, s.cfg.SessionTTL)
	s.log.Info("initial setup completed", "email", u.Email)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	u, err := s.store.GetUserByEmail(r.Context(), strings.TrimSpace(strings.ToLower(in.Email)))
	if err != nil || u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	id, err := s.store.CreateSession(r.Context(), u.ID, s.cfg.SessionTTL)
	if err != nil {
		s.fail(w, err, "create session")
		return
	}
	s.setSessionCookie(w, id, s.cfg.SessionTTL)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	s.setSessionCookie(w, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFrom(r.Context()))
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	var in struct {
		Name            string `json:"name"`
		Email           string `json:"email"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Name != "" {
		u.Name = in.Name
	}
	if in.Email != "" {
		u.Email = strings.ToLower(strings.TrimSpace(in.Email))
	}
	if in.NewPassword != "" {
		if u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.CurrentPassword)) != nil {
			writeErr(w, http.StatusBadRequest, "current password is incorrect")
			return
		}
		if len(in.NewPassword) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err, "update user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// ---- users ----

// handleListUsers is open to every signed-in person so they can share with others.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, "list users")
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if u := userFrom(r.Context()); u == nil || u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "only an admin can do that")
		return false
	}
	return true
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.Email == "" {
		writeErr(w, http.StatusBadRequest, "email is required")
		return
	}
	if s.cfg.AuthMode == "local" && in.Password != "" && len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if in.Role != "admin" {
		in.Role = "member"
	}
	u := &store.User{Email: in.Email, Name: strings.TrimSpace(in.Name), Role: in.Role}
	if in.Password != "" {
		h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		s.fail(w, err, "create user")
		return
	}
	s.log.Info("user created", "email", u.Email, "role", u.Role, "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	u, err := s.store.GetUser(r.Context(), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Email != "" {
		u.Email = strings.TrimSpace(strings.ToLower(in.Email))
	}
	if in.Name != "" {
		u.Name = strings.TrimSpace(in.Name)
	}
	if in.Role == "admin" || in.Role == "member" {
		if in.Role == "member" && u.Role == "admin" {
			if n, _ := s.store.CountAdmins(r.Context()); n <= 1 {
				writeErr(w, http.StatusBadRequest, "there must be at least one admin")
				return
			}
		}
		u.Role = in.Role
	}
	if in.Password != "" {
		if len(in.Password) < 8 {
			writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		u.PasswordHash = string(h)
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		s.fail(w, err, "update user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := idParam(r, "id")
	if id == userFrom(r.Context()).ID {
		writeErr(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	target, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err, "get user")
		return
	}
	if target.Role == "admin" {
		if n, _ := s.store.CountAdmins(r.Context()); n <= 1 {
			writeErr(w, http.StatusBadRequest, "there must be at least one admin")
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.fail(w, err, "delete user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- personal API tokens ----

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.ListAPITokens(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "list tokens")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	_ = decode(r, &in)
	if in.Name == "" {
		in.Name = "token"
	}
	plain, t, err := s.store.CreateAPIToken(r.Context(), userFrom(r.Context()).ID, in.Name)
	if err != nil {
		s.fail(w, err, "create token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": plain, "info": t})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIToken(r.Context(), userFrom(r.Context()).ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "delete token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
