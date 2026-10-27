package gateway

import (
	"errors"
	"net/http"

	"github.com/ratsdev/llm-proxy/internal/convert"
	"github.com/ratsdev/llm-proxy/internal/util"
)

const maxRequestBodyBytes int64 = 64 << 20

// fail renders a gateway error in the inbound protocol's error envelope.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, gatewayError *util.Error) {
	for key, value := range gatewayError.Headers {
		w.Header().Set(key, value)
	}
	payload := convert.FormatError(wireForPath(r.URL.Path),
		gatewayError.Status, gatewayError.Message,
		gatewayError.Type, gatewayError.Code, requestInfoFrom(r).id)
	util.WriteJSON(w, gatewayError.Status, payload)
}

// readJSONBody reads the request body and renders failures in the gateway's
// JSON error envelope.
func readJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, *util.Error) {
	object, err := util.ReadJSONObject(w, r, maxRequestBodyBytes)
	switch {
	case err == nil:
		return object, nil
	case errors.Is(err, util.ErrBodyTooLarge):
		return nil, util.New(413, "Request body too large.", "", "request_too_large", nil)
	case errors.Is(err, util.ErrBodyNotObject):
		return nil, util.New(400, "Request body must be a JSON object.", "", "invalid_body", nil)
	default:
		return nil, util.New(400, "Request body is not valid JSON.", "", "invalid_json", nil)
	}
}

// inboundHeaders forwards the Anthropic protocol headers a caller may set.
func inboundHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string, 2)
	for _, name := range []string{"anthropic-version", "anthropic-beta"} {
		if value := r.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return headers
}
