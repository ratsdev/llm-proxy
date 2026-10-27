package gateway

import (
	"net/http"
	"sort"
	"time"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/ui"
	"github.com/ratsdev/llm-proxy/internal/util"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, http.StatusOK, map[string]any{
		"name":       "llm-proxy",
		"version":    Version,
		"models":     len(s.cfg.Models),
		"endpoints":  advertisedEndpoints(),
		"converters": convert.Definitions(),
		"ui":         ui.RootPath,
	})
}

func advertisedEndpoints() map[string][]string {
	endpoints := make(map[string][]string)
	for _, spec := range routeSpecs {
		if spec.Advertise {
			key := string(spec.Wire)
			endpoints[key] = append(endpoints[key], spec.Inbound)
		}
	}
	return endpoints
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	util.WriteJSON(w, http.StatusOK, map[string]any{
		"status":    "healthy",
		"models":    len(s.cfg.Models),
		"providers": len(s.cfg.Providers),
	})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	log := logFrom(r)
	log.enabled = true

	user, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.user = userID(user)

	ids := make([]string, 0, len(s.cfg.Models))
	for id, model := range s.cfg.Models {
		if user == nil || user.CanUse(model.ID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, modelPayload(s.cfg.Models[id], s.startedAt))
	}
	util.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleRetrieveModel(w http.ResponseWriter, r *http.Request) {
	log := logFrom(r)
	log.enabled = true

	user, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.user = userID(user)
	model, apiErr := s.resolveModel(r.PathValue("model_id"), user)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.model = model.ID
	util.WriteJSON(w, http.StatusOK, modelPayload(model, s.startedAt))
}

// modelPayload renders one model entry. created is the process start time, so
// the listing is stable for the lifetime of the process instead of changing on
// every call.
func modelPayload(model *Model, created time.Time) map[string]any {
	return map[string]any{
		"id":                model.ID,
		"name":              model.Name,
		"object":            "model",
		"created":           created.Unix(),
		"owned_by":          "llm-proxy",
		"modalities":        model.Modalities,
		"output_modalities": model.OutputModalities,
		"reasoning":         model.Reasoning,
		"tools":             model.Tools,
		"context_length":    util.Nullable(model.ContextLength),
		"max_output_length": util.Nullable(model.MaxOutputLength),
	}
}
