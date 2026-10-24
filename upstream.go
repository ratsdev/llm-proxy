package llmproxy

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ratsdev/llm-proxy/util"
)

// maxResponseBodyBytes bounds a buffered upstream reply. Streaming replies are
// relayed incrementally and never buffered, so only complete JSON bodies (and
// streams a provider refused to stream) are subject to this limit.
const maxResponseBodyBytes int64 = 64 << 20

// readResult buffers the upstream body and maps read failures to a 502.
func readResult(result *RouteResult) ([]byte, *util.Error) {
	data, exceeded, err := readCapped(result.Body, maxResponseBodyBytes)
	if err != nil {
		return nil, util.New(502, "Upstream response could not be read.", "api_error", "upstream_error", nil)
	}
	if exceeded {
		return nil, util.New(502, "Upstream response is too large.", "api_error", "upstream_error", nil)
	}
	return data, nil
}

// readCapped reads at most limit bytes and reports whether the body held more.
func readCapped(body io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	return data, int64(len(data)) > limit, nil
}

// decodeObject decodes a JSON object.
func decodeObject(data []byte) (map[string]any, *util.Error) {
	payload, err := util.Decode(data)
	object, ok := util.AsMap(payload)
	if err != nil || !ok {
		return nil, util.New(502, "Upstream returned invalid JSON.", "api_error", "upstream_error", nil)
	}
	return object, nil
}

// parseUpstreamObject decodes a JSON object and turns an embedded error
// envelope into a gateway error, so a 200 response carrying {"error": ...} is
// not mistaken for a successful reply.
func parseUpstreamObject(data []byte) (map[string]any, *util.Error) {
	object, apiErr := decodeObject(data)
	if apiErr != nil {
		return nil, apiErr
	}
	if raw, exists := object["error"]; exists && util.Truthy(raw) {
		message := "Upstream returned an error."
		if errorObject, ok := util.AsMap(raw); ok {
			if text := util.StringValue(errorObject["message"]); text != "" {
				message = text
			}
		}
		return nil, util.New(502, message, "api_error", "upstream_error", nil)
	}
	return object, nil
}

func withProviderHeaders(apiErr *util.Error, result *RouteResult) *util.Error {
	apiErr.Headers = providerHeaders(result)
	return apiErr
}

func upstreamError(result *RouteResult) *util.Error {
	status := result.Status
	data, _ := result.ReadAll()

	var (
		message   string
		errorType string
		code      string
	)
	if decoded, err := util.Decode(data); err == nil {
		if object, ok := util.AsMap(decoded); ok {
			switch errValue := object["error"].(type) {
			case map[string]any:
				message = util.StringValue(errValue["message"])
				errorType = util.StringValue(errValue["type"])
				code = util.StringValue(errValue["code"])
			case string:
				message = errValue
			}
			if message == "" {
				message = util.StringValue(object["message"])
			}
		}
	}
	if message == "" {
		text := strings.TrimSpace(string(data))
		runes := []rune(text)
		if len(runes) > 500 {
			runes = runes[:500]
		}
		message = strings.TrimSpace(string(runes))
	}
	if message == "" {
		message = fmt.Sprintf("Upstream returned HTTP %d.", status)
	}
	if errorType == "" {
		errorType = defaultErrorType(status)
	}
	return util.New(status, message, errorType, code, providerHeaders(result))
}

func defaultErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

func providerHeaders(result *RouteResult) map[string]string {
	headers := map[string]string{
		"x-provider-id":    result.ProviderID,
		"x-upstream-model": result.UpstreamModel,
	}
	if retryAfter := result.Header.Get("Retry-After"); retryAfter != "" {
		headers["Retry-After"] = retryAfter
	}
	return headers
}
