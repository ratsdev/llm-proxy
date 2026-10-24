// Package protocols defines the contract between the gateway IR and concrete
// wire formats. Concrete adapters live in sub-packages (openai, anthropic) so
// that each vendor's conversion rules stay isolated.
package protocols

import "github.com/ratsdev/llm-proxy/ir"

// Decoder converts upstream SSE events into IR stream events.
type Decoder interface {
	// Feed consumes one SSE event and returns zero or more IR events.
	Feed(event, data string) ([]ir.StreamEvent, error)
	// Finish closes any blocks still open and emits the terminal events.
	Finish() []ir.StreamEvent
}

// Encoder converts IR stream events into encoded SSE frames.
type Encoder interface {
	// Encode consumes one IR event and returns zero or more SSE frames.
	Encode(ev ir.StreamEvent) ([][]byte, error)
	// EncodeStreamError emits the protocol's error event without starting the
	// stream. It is used when a passthrough stream breaks after data has
	// already been forwarded, where starting the encoder would add a bogus
	// message/response header.
	EncodeStreamError(message string) [][]byte
	// Finish emits any terminal frames that have not been written yet.
	Finish() [][]byte
}

// Adapter converts between one wire format and the IR.
type Adapter interface {
	ParseRequest(body map[string]any) (*ir.Request, error)
	FormatRequest(req *ir.Request, upstreamModel string, maxTokensDefault *int) (map[string]any, error)
	ParseResponse(payload map[string]any) (*ir.Response, error)
	FormatResponse(resp *ir.Response, model string) map[string]any
	FormatError(status int, message, errorType, code, requestID string) map[string]any
	// RewriteModel replaces the public model name wherever the wire format
	// embeds it (top level or nested in message/response envelopes). It
	// reports whether anything changed.
	RewriteModel(payload map[string]any, model string) bool
	NewDecoder() Decoder
	NewEncoder(model string, includeUsage bool) Encoder
}
