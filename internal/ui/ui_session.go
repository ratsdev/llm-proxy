package ui

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
	"github.com/ratsdev/llm-proxy/internal/util"
)

// dummyPasswordHash is verified for unknown accounts and accounts without a
// password. Running the same PBKDF2 work on every failed lookup keeps response
// timing from revealing which usernames exist.
var dummyPasswordHash = sync.OnceValue(func() string {
	hash, err := core.HashPassword("llm-proxy-dummy-verifier")
	if err != nil {
		return ""
	}
	return hash
})

// state holds the in-memory session table and the bounded request history.
// Neither survives a restart: the gateway stays stateless.
type state struct {
	mu       sync.Mutex
	sessions map[string]consoleSession
	records  []RequestRecord
	next     int
}

func newState() *state {
	return &state{
		sessions: make(map[string]consoleSession),
		records:  make([]RequestRecord, 0, historySize),
	}
}

type consoleSession struct {
	userID    string
	expiresAt time.Time
}

func (u *state) createSession(userID string, now time.Time) (string, time.Time, error) {
	suffix, err := util.RandomHex(48)
	if err != nil {
		// A session token is a bearer credential: refusing to issue one is the
		// only safe response to a failed entropy source.
		return "", time.Time{}, err
	}
	token := "ui_" + suffix
	expires := now.Add(sessionTTL)

	u.mu.Lock()
	defer u.mu.Unlock()
	for key, session := range u.sessions {
		if !session.expiresAt.After(now) {
			delete(u.sessions, key)
		}
	}
	if len(u.sessions) >= maxSessions {
		oldestKey := ""
		var oldest time.Time
		for key, session := range u.sessions {
			if oldestKey == "" || session.expiresAt.Before(oldest) {
				oldestKey, oldest = key, session.expiresAt
			}
		}
		if oldestKey != "" {
			delete(u.sessions, oldestKey)
		}
	}
	u.sessions[token] = consoleSession{userID: userID, expiresAt: expires}
	return token, expires, nil
}

func (u *state) lookupSession(token string, now time.Time) (consoleSession, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	session, ok := u.sessions[token]
	if !ok {
		return consoleSession{}, false
	}
	if !session.expiresAt.After(now) {
		delete(u.sessions, token)
		return consoleSession{}, false
	}
	return session, true
}

func (u *state) deleteSession(token string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.sessions, token)
}

// deleteOtherSessions invalidates every session for a user except keep. It is
// used after a password change.
func (u *state) deleteOtherSessions(userID, keep string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for token, session := range u.sessions {
		if token != keep && session.userID == userID {
			delete(u.sessions, token)
		}
	}
}

func (h *Handler) logLogin(r *http.Request, userID string) {
	if h.onLogin != nil {
		h.onLogin(r, userID)
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	h.logLogin(r, "")
	if h.registrationRequired() {
		writeError(w, http.StatusConflict,
			"No administrator account exists yet. Register one to get started.", "registration_required")
		return
	}

	users := h.usersByID()
	if !consoleEnabled(users) {
		writeError(w, http.StatusForbidden, disabledMessage, "ui_disabled")
		return
	}

	object, ok := readObjectLimited(w, r, maxLoginBody)
	if !ok {
		return
	}

	username := strings.TrimSpace(util.StringValue(object["username"]))
	password := util.StringValue(object["password"])
	if username == "" && password == "" {
		writeError(w, http.StatusBadRequest, "Enter your username and password to continue.", "missing_credentials")
		return
	}
	// Throttling runs before the password check so a guessing loop costs the
	// attacker time instead of a PBKDF2 derivation per attempt. A rejected
	// sign-in is counted per account and per source address: the account bucket
	// stops a distributed attack, and the address bucket stops one address from
	// spraying usernames.
	keys := limiterKeys(r, username)
	if wait := h.logins.reserve(keys, time.Now()); wait > 0 {
		w.Header().Set("retry-after", retryAfterSeconds(wait))
		writeError(w, http.StatusTooManyRequests, "Too many sign-in attempts. Wait a moment and try again.", "too_many_attempts")
		return
	}
	user, found := users[username]
	verifier := dummyPasswordHash()
	active := found && user.PasswordHash != ""
	if active {
		verifier = user.PasswordHash
	}
	valid := core.VerifyPassword(verifier, password)
	if !active || !valid {
		writeError(w, http.StatusUnauthorized, "The username or password isn't valid.", "invalid_credentials")
		return
	}
	h.logins.reset(keys)
	h.startSession(w, r, user)
}

// handleRegister creates the first administrator account from the console.
// Registration stays open until an administrator exists, so a fresh deployment
// can be claimed by the first visitor; the store performs the final check
// atomically.
func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	h.logLogin(r, "")
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "The configuration store is unavailable.", "store_unavailable")
		return
	}
	firstRun := h.registrationRequired()
	cfg := h.config()
	if !firstRun && !cfg.SelfRegistration {
		writeError(w, http.StatusConflict,
			"Self-registration is disabled. Ask an administrator for an account.", "registration_closed")
		return
	}

	object, ok := readObjectLimited(w, r, maxLoginBody)
	if !ok {
		return
	}

	username := strings.TrimSpace(util.StringValue(object["username"]))
	name := strings.TrimSpace(util.StringValue(object["name"]))
	password := util.StringValue(object["password"])
	if username == "" {
		writeError(w, http.StatusBadRequest, "Choose a username to continue.", "missing_username")
		return
	}
	if err := core.ValidateUserID(username); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_username")
		return
	}
	if len(password) < core.MinPasswordLength {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("The password must be at least %d characters.", core.MinPasswordLength), "weak_password")
		return
	}
	// Account creation derives a PBKDF2 verifier and writes a row, so every
	// accepted attempt spends a per-address budget instead of letting one visitor
	// create accounts without limit.
	if wait := h.registrations.reserve(limiterKeys(r, ""), time.Now()); wait > 0 {
		w.Header().Set("retry-after", retryAfterSeconds(wait))
		writeError(w, http.StatusTooManyRequests,
			"Too many registration attempts from this address. Wait a moment and try again.", "too_many_attempts")
		return
	}
	input := core.UserInput{ID: username, Name: name, Password: password}
	var (
		user core.UserInput
		err  error
	)
	if firstRun {
		user, err = h.store.RegisterAdmin(input)
	} else {
		user, err = h.store.RegisterUser(input, cfg.DefaultAllowedModels)
	}
	if err != nil {
		writeRegistrationError(w, err)
		return
	}
	if err := h.afterChange(); err != nil {
		writeError(w, http.StatusInternalServerError,
			"The account was created but the configuration could not be activated: "+err.Error(), "reload_failed")
		return
	}
	h.startSession(w, r, &User{
		ID:            user.ID,
		Name:          user.Name,
		AllowedModels: user.AllowedModels,
		IsAdmin:       user.IsAdmin,
	})
}

// handleRegistrationStatus describes whether the console still needs its
// first administrator or accepts self-service member registration. It is public
// so the sign-in screen can offer the right action, so it deliberately returns
// only those availability flags and never registration defaults.
func (h *Handler) handleRegistrationStatus(w http.ResponseWriter, r *http.Request) {
	cfg := h.config()
	firstRun := h.store != nil && !cfg.HasAdmin
	writeJSON(w, http.StatusOK, map[string]any{
		"first_run": firstRun,
		"enabled":   h.store != nil && (firstRun || cfg.SelfRegistration),
	})
}

// startSession issues the session cookie and returns the same payload as a
// successful sign-in, so registration can drop the operator straight into the
// console.
func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, user *User) {
	token, expires, err := h.ui.createSession(user.ID, time.Now())
	if err != nil {
		slog.Error("console session could not be created", "user_id", user.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "Could not start a session. Try again.", "session_failed")
		return
	}
	h.logLogin(r, user.ID)
	h.setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       h.userPayload(user),
		"expires_at": expires.UTC(),
		"gateway":    h.gatewayPayload(),
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		h.ui.deleteSession(cookie.Value)
	}
	h.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	user, expires, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          h.userPayload(user),
		"expires_at":    expires.UTC(),
		"gateway":       h.gatewayPayload(),
	})
}

func consoleEnabled(users map[string]*User) bool {
	for _, user := range users {
		if user.PasswordHash != "" {
			return true
		}
	}
	return false
}

// registrationRequired reports whether the console still needs its first
// administrator. The flag is derived when the configuration snapshot loads, so
// this check is constant time. Registration is only meaningful when a
// persistent store is available, because the account must be written before a
// session can exist.
func (h *Handler) registrationRequired() bool {
	return h.store != nil && !h.config().HasAdmin
}

// requireUser enforces the session cookie on every data endpoint. The UI
// never accepts a raw API key after login, so a stolen page cannot be used to
// enumerate configuration without also stealing the HttpOnly cookie.
func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) (*User, time.Time, bool) {
	if h.registrationRequired() {
		writeError(w, http.StatusUnauthorized,
			"Register the first administrator to continue.", "registration_required")
		return nil, time.Time{}, false
	}
	users := h.usersByID()
	if !consoleEnabled(users) {
		writeError(w, http.StatusForbidden, disabledMessage, "ui_disabled")
		return nil, time.Time{}, false
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "Sign in to continue.", "not_authenticated")
		return nil, time.Time{}, false
	}
	session, ok := h.ui.lookupSession(cookie.Value, time.Now())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Your session has expired. Sign in again.", "session_expired")
		return nil, time.Time{}, false
	}
	user, ok := users[session.userID]
	if !ok {
		h.ui.deleteSession(cookie.Value)
		writeError(w, http.StatusUnauthorized, "Your session has expired. Sign in again.", "session_expired")
		return nil, time.Time{}, false
	}
	return user, session.expiresAt, true
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     apiPath,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     apiPath,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(proto, "https")
}

func (h *Handler) gatewayPayload() map[string]any {
	return map[string]any{
		"name":       "llm-proxy",
		"version":    h.version,
		"converters": convert.Definitions(),
	}
}

func (h *Handler) userPayload(user *User) map[string]any {
	return map[string]any{
		"id":       user.ID,
		"name":     user.Name,
		"is_admin": user.IsAdmin,
	}
}
