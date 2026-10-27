package ui

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/util"
)

func (h *Handler) handleUIMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeUIError(w, http.StatusMethodNotAllowed, "Method not allowed.", "method_not_allowed")
}

// parseUIInt reads a bounded query parameter, falling back to the default when
// the value is missing or unparsable.
func parseUIInt(raw string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return min(max(value, minimum), maximum)
}

func writeUIJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("cache-control", uiNoStore)
	w.Header().Set("x-content-type-options", "nosniff")
	util.WriteJSON(w, status, payload)
}

func writeUIError(w http.ResponseWriter, status int, message, code string) {
	writeUIJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"code":    code,
		},
	})
}
