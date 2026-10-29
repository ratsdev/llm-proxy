package gateway

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/router"
	"github.com/ratsdev/llm-proxy/internal/util"
)

var errClientGone = errors.New("client disconnected")

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request, spec routeSpec) {
	log := logFrom(r)
	log.enabled = true

	user, apiErr := s.authenticate(r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	payload, apiErr := readJSONBody(w, r)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}

	modelID, _ := util.AsString(payload["model"])
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		s.fail(w, r, util.New(400, "'model' is required.", "", "model_required", nil))
		return
	}
	model, apiErr := s.resolveModel(modelID, user)
	if apiErr != nil {
		s.fail(w, r, apiErr)
		return
	}
	log.user = userID(user)
	log.model = modelID
	log.inbound = spec.Wire

	stream, _ := util.AsBool(payload["stream"])
	// Ask a Chat Completions upstream for its trailing usage chunk even when the
	// caller did not: the console needs output tokens to report throughput. The
	// relay drops that extra chunk again for callers that never asked for it.
	clientUsage := inboundStreamIncludeUsage(spec.Wire, payload)
	injectedUsage := requestStreamUsage(payload, spec.Wire, stream)
	stickyID := router.SessionKey(r.Header, payload, userID(user), model.ID)
	routeRequest := RouteRequest{
		Model:           model,
		Stream:          stream,
		InboundHeaders:  inboundHeaders(r),
		StickyID:        stickyID,
		PreferredFormat: spec.Wire,
		Payload:         payload,
	}
	log.stream = routeRequest.Stream

	result, err := s.router.Send(r.Context(), routeRequest)
	if err != nil {
		if r.Context().Err() != nil {
			s.logger.Debug("request canceled", "request_id", requestInfoFrom(r).id, "error", err)
			return
		}
		var gatewayError *util.Error
		if errors.As(err, &gatewayError) {
			s.fail(w, r, gatewayError)
			return
		}
		s.logger.Error("upstream dispatch failed", "request_id", requestInfoFrom(r).id, "error", err)
		s.fail(w, r, util.New(500, "Internal server error.", "api_error", "internal_error", nil))
		return
	}
	log.provider = result.ProviderID
	log.upstreamModel = result.UpstreamModel
	log.upstream = result.WireFormat
	if !result.OK() {
		s.fail(w, r, upstreamError(result))
		_ = result.Close()
		return
	}

	w.Header().Set("x-provider-id", result.ProviderID)
	w.Header().Set("x-upstream-model", result.UpstreamModel)

	relay := &relaySpec{
		mode:           planMode(result.WireFormat == spec.Wire, isJSONResponse(result)),
		modelID:        modelID,
		inbound:        spec.Wire,
		upstream:       result.WireFormat,
		includeUsage:   clientUsage,
		dropUsageChunk: injectedUsage && !clientUsage,
		log:            log,
	}
	s.relay(w, r, result, relay)
}

// requestStreamUsage sets stream_options.include_usage on a streamed Chat
// Completions request. Most providers only append their token usage when the
// flag is set, and the console derives output throughput from it. It reports
// whether the flag was added.
func requestStreamUsage(payload map[string]any, wire WireFormat, stream bool) bool {
	if !stream || wire != wireChatCompletions {
		return false
	}
	options, _ := util.AsMap(payload["stream_options"])
	if util.Truthy(options["include_usage"]) {
		return false
	}
	if options == nil {
		options = make(map[string]any, 1)
		payload["stream_options"] = options
	}
	options["include_usage"] = true
	return true
}
