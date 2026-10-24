package ir

// StreamEvents expands a non-streaming response into the same event sequence a
// streaming upstream would have produced. It lets the gateway answer a caller
// that asked for a stream when the upstream ignored that request and returned
// an ordinary JSON body.
func (r *Response) StreamEvents() []StreamEvent {
	events := []StreamEvent{{
		Type:      MessageStart,
		MessageID: r.ID,
		Model:     r.Model,
		Created:   r.Created,
	}}
	for index, block := range r.Content {
		switch block.Type {
		case Text, Thinking, ToolUse:
		default:
			continue
		}
		current := block
		events = append(events, StreamEvent{Type: ContentBlockStart, Index: index, Block: &current})
		switch block.Type {
		case Text:
			if block.Text != "" {
				events = append(events, StreamEvent{
					Type: ContentBlockDelta, Index: index, DeltaType: DeltaText, Delta: block.Text,
				})
			}
		case Thinking:
			if block.Text != "" {
				events = append(events, StreamEvent{
					Type: ContentBlockDelta, Index: index, DeltaType: DeltaThinking, Delta: block.Text,
				})
			}
			if block.Signature != "" {
				events = append(events, StreamEvent{
					Type: ContentBlockDelta, Index: index, DeltaType: DeltaSignature, Delta: block.Signature,
				})
			}
		case ToolUse:
			if block.ToolInput != "" {
				events = append(events, StreamEvent{
					Type: ContentBlockDelta, Index: index, DeltaType: DeltaInputJSON, Delta: block.ToolInput,
				})
			}
		}
		events = append(events, StreamEvent{Type: ContentBlockStop, Index: index})
	}
	usage := r.Usage
	events = append(events,
		StreamEvent{
			Type:         MessageDelta,
			StopReason:   r.StopReason,
			StopSequence: r.StopSequence,
			Usage:        &usage,
		},
		StreamEvent{Type: MessageStop},
	)
	return events
}
