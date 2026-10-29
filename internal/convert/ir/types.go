package ir

import "strings"

// Content block types.
const (
	Text       = "text"
	Image      = "image"
	Video      = "video"
	ToolUse    = "tool_use"
	ToolResult = "tool_result"
	Thinking   = "thinking"
)

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Canonical stop reasons, independent of any wire protocol.
const (
	EndTurn      = "end_turn"
	MaxTokens    = "max_tokens"
	ToolUseStop  = "tool_use"
	StopSequence = "stop_sequence"
	Refusal      = "refusal"
)

// Canonical stream event types.
const (
	MessageStart      = "message_start"
	ContentBlockStart = "content_block_start"
	ContentBlockDelta = "content_block_delta"
	ContentBlockStop  = "content_block_stop"
	MessageDelta      = "message_delta"
	MessageStop       = "message_stop"
	StreamError       = "error"
)

// Canonical delta types carried by ContentBlockDelta.
const (
	DeltaText      = "text"
	DeltaThinking  = "thinking"
	DeltaSignature = "signature"
	DeltaInputJSON = "input_json"
)

// Block is one piece of message content.
type Block struct {
	Type      string
	Text      string
	ImageURL  string
	VideoURL  string
	MediaType string
	Data      string
	ToolUseID string
	ToolName  string
	ToolInput string
	IsError   bool
	Signature string
	// CacheControl carries Anthropic prompt-cache hints across a round trip.
	CacheControl map[string]any
}

// Message is one conversation turn.
type Message struct {
	Role    string
	Content []Block
}

// Tool is a callable tool exposed to the model.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Request is a normalised chat-family request.
type Request struct {
	Messages     []Message
	System       []Block
	MaxTokens    *int
	Temperature  *float64
	TopP         *float64
	TopK         *int
	Stop         []string
	Tools        []Tool
	ToolChoice   *string
	Stream       bool
	IncludeUsage bool
	Extra        map[string]any
}

// Response is a normalised non-streaming response.
type Response struct {
	ID           string
	Model        string
	Content      []Block
	StopReason   *string
	StopSequence *string
	Usage        Usage
	Created      int64
}

// StreamEvent is one normalised streaming event.
type StreamEvent struct {
	Type         string
	Index        int
	Block        *Block
	DeltaType    string
	Delta        string
	MessageID    string
	Model        string
	Created      int64
	StopReason   *string
	StopSequence *string
	Usage        *Usage
	Error        string
	ErrorType    string
}

// Content block builders and helpers.

// ToolBlockIndexBase offsets streamed tool blocks so they do not collide with
// the fixed text (0) and thinking (1) IR indexes used by the OpenAI adapters.
const ToolBlockIndexBase = 1000

// TextBlock builds a text content block.
func TextBlock(text string) Block {
	return Block{Type: Text, Text: text}
}

// JoinText joins the non-empty text blocks with newlines.
func JoinText(blocks []Block) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == Text && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
