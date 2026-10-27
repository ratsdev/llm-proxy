package router

import (
	"net/http"
	"time"

	"github.com/ratsdev/llm-proxy/internal/version"
)

// upstreamHeaders builds the upstream request headers for one attempt. The
// inbound Anthropic version and beta flags are forwarded so a passthrough
// request keeps the caller's protocol features.
func upstreamHeaders(provider *Provider, wire WireFormat, stream bool, inbound map[string]string) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}
	headers.Set("User-Agent", "llm-proxy/"+version.Value)

	if provider.APIKey != "" {
		if wire == wireMessages {
			headers.Set("x-api-key", provider.APIKey)
		} else {
			headers.Set("Authorization", "Bearer "+provider.APIKey)
		}
	}
	if wire == wireMessages {
		version := inbound["anthropic-version"]
		if version == "" {
			version = defaultAnthropicVersion
		}
		headers.Set("anthropic-version", version)
		if beta := inbound["anthropic-beta"]; beta != "" {
			headers.Set("anthropic-beta", beta)
		}
	}
	// Configured headers are applied last so operators can override anything
	// above, including the Authorization/Accept defaults set here.
	for key, value := range provider.Headers {
		headers.Set(key, value)
	}
	return headers
}

// readTimeoutFor resolves the upstream deadline: model, then provider, then the
// gateway default.
func readTimeoutFor(model *Model, provider *Provider) time.Duration {
	switch {
	case model.Timeout != nil && *model.Timeout > 0:
		return time.Duration(*model.Timeout * float64(time.Second))
	case provider.Timeout != nil && *provider.Timeout > 0:
		return time.Duration(*provider.Timeout * float64(time.Second))
	default:
		return DefaultReadTimeout
	}
}

// maxRetryAfterFor reports how long one model provider binding may wait for a
// Retry-After before retrying the same upstream.
func maxRetryAfterFor(modelProvider ModelProvider) time.Duration {
	if modelProvider.MaxRetryAfter == nil {
		return defaultMaxRetryAfter
	}
	return time.Duration(*modelProvider.MaxRetryAfter * float64(time.Second))
}
