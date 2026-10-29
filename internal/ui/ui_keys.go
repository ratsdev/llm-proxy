package ui

import (
	"net/http"

	"github.com/ratsdev/llm-proxy/internal/util"
)

func (h *Handler) handleListKeys(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUIUserStore(w, r)
	if !ok {
		return
	}
	keys, err := h.store.ListAPIKeys(user.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (h *Handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUIUserStore(w, r)
	if !ok {
		return
	}
	object, ok := readUIObject(w, r)
	if !ok {
		return
	}
	key, err := h.store.CreateAPIKey(user.ID, util.StringValue(object["name"]))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "API key created.", map[string]any{"key": key})
}

func (h *Handler) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUIUserStore(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteAPIKey(user.ID, r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	h.saveAndReload(w, "API key deleted.", nil)
}
