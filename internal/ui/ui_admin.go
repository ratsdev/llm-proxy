package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/core"
	"github.com/ratsdev/llm-proxy/internal/util"
)

const uiMaxAdminBody = 1 << 20

func (h *Handler) requireUIAdmin(w http.ResponseWriter, r *http.Request) (*User, bool) {
	user, _, ok := h.requireUIUser(w, r)
	if !ok {
		return nil, false
	}
	if !user.IsAdmin {
		writeUIError(w, http.StatusForbidden, "Administrator access is required.", "forbidden")
		return nil, false
	}
	return user, true
}

// requireUIUserStore requires the signed-in account and the persistent
// configuration store, which every account-scoped write and key operation needs.
func (h *Handler) requireUIUserStore(w http.ResponseWriter, r *http.Request) (*User, bool) {
	user, _, ok := h.requireUIUser(w, r)
	if !ok {
		return nil, false
	}
	if h.store == nil {
		writeUIError(w, http.StatusServiceUnavailable, "The configuration store is unavailable.", "store_unavailable")
		return nil, false
	}
	return user, true
}

// requireUIAdminStore additionally requires administrator privileges.
func (h *Handler) requireUIAdminStore(w http.ResponseWriter, r *http.Request) (*User, bool) {
	user, ok := h.requireUIUserStore(w, r)
	if !ok {
		return nil, false
	}
	if !user.IsAdmin {
		writeUIError(w, http.StatusForbidden, "Administrator access is required.", "forbidden")
		return nil, false
	}
	return user, true
}

func (h *Handler) afterChange() error {
	if h.reload == nil {
		return nil
	}
	return h.reload()
}

func readUIObject(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	return readUIObjectLimited(w, r, uiMaxAdminBody)
}

// readUIObjectLimited decodes a console JSON body. Callers choose the limit so
// sign-in and registration stay small without loosening administrative writes.
func readUIObjectLimited(w http.ResponseWriter, r *http.Request, limit int64) (map[string]any, bool) {
	object, err := util.ReadJSONObject(w, r, limit)
	switch {
	case err == nil:
		return object, true
	case errors.Is(err, util.ErrBodyTooLarge):
		writeUIError(w, http.StatusRequestEntityTooLarge, "Request body is too large.", "invalid_body")
	case errors.Is(err, util.ErrBodyUnreadable):
		writeUIError(w, http.StatusBadRequest, "Could not read the request body.", "invalid_body")
	default:
		writeUIError(w, http.StatusBadRequest, "Request body must be a JSON object.", "invalid_json")
	}
	return nil, false
}

func decodeUI[T any](w http.ResponseWriter, object map[string]any) (T, bool) {
	var target T
	encoded, err := util.Marshal(object)
	if err != nil {
		writeUIError(w, http.StatusBadRequest, "Request body could not be encoded.", "invalid_body")
		return target, false
	}
	if err := json.Unmarshal(encoded, &target); err != nil {
		writeUIError(w, http.StatusBadRequest, "Request body has the wrong shape.", "invalid_body")
		return target, false
	}
	return target, true
}

func (h *Handler) saveAndReload(w http.ResponseWriter, message string, payload map[string]any) {
	if err := h.afterChange(); err != nil {
		writeUIError(w, http.StatusInternalServerError,
			"The configuration was saved but could not be activated: "+err.Error(), "reload_failed")
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["ok"] = true
	if message != "" {
		payload["message"] = message
	}
	writeUIJSON(w, http.StatusOK, payload)
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeUIError(w, http.StatusNotFound, "Not found.", "not_found")
		return
	}
	writeUIError(w, http.StatusBadRequest, err.Error(), "invalid_configuration")
}

// writeRegistrationError maps account-registration failures onto the statuses
// the sign-in screen distinguishes. Registration never confirms whether a
// username exists: a taken name returns the same generic conflict as any other
// rejected attempt, so the public form cannot be used to enumerate accounts.
// The remaining failures come from the store or the entropy source, so they are
// internal errors rather than bad input.
func writeRegistrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrAdminExists):
		writeUIError(w, http.StatusConflict, "The console is already set up. Sign in instead.", "registration_closed")
	case errors.Is(err, core.ErrUserIDTaken):
		writeUIError(w, http.StatusConflict,
			"The account could not be created. Sign in if you already have an account.", "registration_failed")
	default:
		slog.Error("console registration failed", "error", err)
		writeUIError(w, http.StatusInternalServerError, "The account could not be created.", "internal_error")
	}
}

// ---------------------------------------------------------------- providers

func (h *Handler) handleUIGetProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	input, err := h.store.GetProvider(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"provider": input})
}

func (h *Handler) handleUISaveProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	object, ok := readUIObject(w, r)
	if !ok {
		return
	}
	input, ok := decodeUI[ProviderInput](w, object)
	if !ok {
		return
	}
	if id := r.PathValue("id"); id != "" {
		input.ID = id
	}
	saved, err := h.store.SaveProvider(input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "Provider saved.", map[string]any{"provider": saved})
}

func (h *Handler) handleUIDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	if err := h.store.DeleteProvider(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "Provider deleted.", nil)
}

// ------------------------------------------------------------------- models

func (h *Handler) handleUIGetModel(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	input, err := h.store.GetModel(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"model": input})
}

func (h *Handler) handleUISaveModel(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	object, ok := readUIObject(w, r)
	if !ok {
		return
	}
	input, ok := decodeUI[ModelInput](w, object)
	if !ok {
		return
	}
	if id := r.PathValue("id"); id != "" {
		input.ID = id
	}
	saved, err := h.store.SaveModel(input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "Model saved.", map[string]any{"model": saved})
}

func (h *Handler) handleUIDeleteModel(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	if err := h.store.DeleteModel(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "Model deleted.", nil)
}

// -------------------------------------------------------------------- users

func (h *Handler) handleUIListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	users, err := h.store.ListUsers()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) handleUIGetUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	user, err := h.store.GetUser(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (h *Handler) handleUISaveUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	object, ok := readUIObject(w, r)
	if !ok {
		return
	}
	input, ok := decodeUI[UserInput](w, object)
	if !ok {
		return
	}
	if id := r.PathValue("id"); id != "" {
		input.ID = id
	}
	saved, err := h.store.SaveUser(input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "User saved.", map[string]any{"user": saved})
}

func (h *Handler) handleUIDeleteUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	if err := h.store.DeleteUser(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "User deleted.", nil)
}

// ---------------------------------------------------------------- settings

func (h *Handler) handleUIGetSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	settings, err := h.store.RegistrationSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeUIJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

func (h *Handler) handleUISaveSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdminStore(w, r); !ok {
		return
	}
	object, ok := readUIObject(w, r)
	if !ok {
		return
	}
	input, ok := decodeUI[core.RegistrationSettings](w, object)
	if !ok {
		return
	}
	saved, err := h.store.SaveRegistrationSettings(input)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "Registration settings saved.", map[string]any{"settings": saved})
}

// ----------------------------------------------------------------- password

func (h *Handler) handleUIChangePassword(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.requireUIUser(w, r)
	if !ok {
		return
	}
	if h.store == nil {
		writeUIError(w, http.StatusServiceUnavailable, "The configuration store is unavailable.", "store_unavailable")
		return
	}
	object, ok := readUIObject(w, r)
	if !ok {
		return
	}
	current := util.StringValue(object["current_password"])
	next := util.StringValue(object["new_password"])
	if !core.VerifyPassword(user.PasswordHash, current) {
		writeUIError(w, http.StatusUnauthorized, "The current password isn't valid.", "invalid_password")
		return
	}
	if len(next) < core.MinPasswordLength {
		writeUIError(w, http.StatusBadRequest, fmt.Sprintf("The new password must be at least %d characters.", core.MinPasswordLength), "weak_password")
		return
	}
	if err := h.store.SetPassword(user.ID, next); err != nil {
		writeStoreError(w, err)
		return
	}
	// Every other session for this account must re-authenticate with the new
	// password. The current session is kept so the UI stays usable.
	h.ui.deleteOtherSessions(user.ID, h.currentSessionToken(r))
	h.saveAndReload(w, "Password updated.", nil)
}

func (h *Handler) currentSessionToken(r *http.Request) string {
	cookie, err := r.Cookie(uiSessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// --------------------------------------------------------------- converters

func (h *Handler) handleUIConverters(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUIAdmin(w, r); !ok {
		return
	}
	formats := make([]string, 0, len(convert.Formats()))
	for _, format := range convert.Formats() {
		formats = append(formats, string(format))
	}
	writeUIJSON(w, http.StatusOK, map[string]any{
		"formats":    formats,
		"converters": convert.Definitions(),
		"policy":     "Converters are configured per endpoint. Choose \"none\" to forward the client protocol unchanged; cross-protocol converters map requests, responses and SSE events.",
	})
}
