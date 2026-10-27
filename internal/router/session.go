package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ratsdev/llm-proxy/internal/util"
)

// sessionAffinityHeaders carry a caller-supplied conversation identity. The
// first non-empty value wins. An explicit header is the only way to guarantee
// that every turn of a conversation maps to the same affinity key: deriving a
// key from message content can collide when two conversations open with the
// same text.
var sessionAffinityHeaders = []string{"x-session-id", "x-conversation-id"}

// SessionKey derives the sticky-routing key for one gateway request. Requests
// that belong to the same conversation get the same key, so the router keeps
// them on one upstream provider.
//
// Precedence:
//  1. an explicit x-session-id or x-conversation-id header;
//  2. the OpenAI prompt_cache_key field, which is already an affinity hint;
//  3. a fingerprint of the first user message.
//
// The authenticated subject and the public model are mixed into the digest so
// the in-memory pin cannot leak across tenants or model aliases.
//
// A caller-supplied request id is deliberately not an affinity seed: clients
// and proxies that mint a unique id per request would fill the bounded pin
// cache with one-shot entries and evict the conversations that need it.
func SessionKey(header http.Header, payload map[string]any, subject, modelID string) string {
	for _, name := range sessionAffinityHeaders {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return SessionDigest(modelID, subject, name+"="+value)
		}
	}
	if key := stringValue(payload["prompt_cache_key"]); key != "" {
		return SessionDigest(modelID, subject, "prompt_cache_key="+key)
	}
	if seed := conversationSeed(payload); seed != "" {
		return SessionDigest(modelID, subject, "first-user="+seed)
	}
	return ""
}

// SessionDigest shortens the affinity material to a stable opaque id.
func SessionDigest(modelID, subject, seed string) string {
	sum := sha256.Sum256([]byte(modelID + "\x00" + subject + "\x00" + seed))
	return "sess_" + hex.EncodeToString(sum[:16])
}

// conversationSeed returns a stable representation of the first user turn.
// Chat-completions requests use messages[]. Responses requests use input.
func conversationSeed(payload map[string]any) string {
	if messages, ok := payload["messages"].([]any); ok {
		for _, raw := range messages {
			message, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if strings.EqualFold(stringValue(message["role"]), "user") {
				if seed := contentSeed(message["content"]); seed != "" {
					return seed
				}
			}
		}
	}
	switch input := payload["input"].(type) {
	case string:
		return strings.TrimSpace(input)
	case []any:
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role := stringValue(item["role"])
			if role == "" {
				role = stringValue(item["type"])
			}
			if !strings.EqualFold(role, "user") && !strings.EqualFold(role, "message") {
				continue
			}
			if seed := contentSeed(item["content"]); seed != "" {
				return seed
			}
			if seed := stringValue(item["text"]); seed != "" {
				return seed
			}
		}
	}
	return ""
}

// contentSeed keeps the textual part of a message stable. Multimodal blocks
// without text fall back to their JSON shape, which is deterministic in Go.
func contentSeed(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				if text := stringValue(raw); text != "" {
					parts = append(parts, text)
				}
				continue
			}
			if text := stringValue(block["text"]); text != "" {
				parts = append(parts, text)
				continue
			}
			if text := stringValue(block["content"]); text != "" {
				parts = append(parts, text)
				continue
			}
			if encoded, err := json.Marshal(block); err == nil {
				parts = append(parts, string(encoded))
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if encoded, err := json.Marshal(value); err == nil {
			return string(encoded)
		}
	}
	return ""
}

// stringValue trims a JSON string value; any other type reads as empty.
func stringValue(value any) string {
	return strings.TrimSpace(util.StringValue(value))
}
