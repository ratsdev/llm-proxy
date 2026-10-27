package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// authenticate resolves the caller's API key. An empty key table disables
// authentication entirely, which is what a fresh deployment looks like.
func (s *Server) authenticate(r *http.Request) (*User, *util.Error) {
	if !s.cfg.AuthEnabled() {
		return nil, nil
	}
	authorization := r.Header.Get("Authorization")
	var token string
	if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		token = strings.TrimSpace(authorization[7:])
	} else {
		token = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if token == "" {
		return nil, util.New(401,
			"Missing API key. Pass it as 'Authorization: Bearer <key>' or 'x-api-key'.",
			"authentication_error", "missing_api_key", nil)
	}
	user, ok := s.cfg.UsersByKey[token]
	if !ok {
		return nil, util.New(401, "Invalid API key.", "authentication_error", "invalid_api_key", nil)
	}
	return user, nil
}

// resolveModel enforces the account's model allow-list.
func (s *Server) resolveModel(modelID string, user *User) (*Model, *util.Error) {
	model, ok := s.cfg.Models[modelID]
	if !ok {
		return nil, util.New(404, fmt.Sprintf("Model '%s' not found.", modelID),
			"model_not_found", "model_not_found", nil)
	}
	if user != nil && !user.CanUse(model.ID) {
		return nil, util.New(403, fmt.Sprintf("User '%s' is not allowed to use model '%s'.", user.ID, model.ID),
			"permission_error", "model_not_allowed", nil)
	}
	return model, nil
}

func userID(user *User) string {
	if user == nil {
		return "-"
	}
	return user.ID
}
