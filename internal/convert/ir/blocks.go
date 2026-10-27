package ir

import "strings"

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
