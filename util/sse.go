package util

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// DefaultMaxSSELineBytes bounds a single SSE line to keep a misbehaving
// upstream from exhausting memory.
const DefaultMaxSSELineBytes = 8 << 20

// DefaultMaxSSEEventBytes bounds one event's accumulated data. A line limit
// alone is not enough: an upstream can keep sending short data lines without
// ever dispatching, so the event budget must be enforced separately.
const DefaultMaxSSEEventBytes = 8 << 20

// Frame serialises one server-sent event. A string payload is written
// verbatim (used for the "[DONE]" sentinel); anything else is JSON encoded.
func Frame(event string, payload any) ([]byte, error) {
	var data string
	if raw, ok := payload.(string); ok {
		data = raw
	} else {
		encoded, err := Marshal(payload)
		if err != nil {
			return nil, err
		}
		data = string(encoded)
	}

	var b strings.Builder
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\n")
	}
	b.WriteString("data: ")
	b.WriteString(data)
	b.WriteString("\n\n")
	return []byte(b.String()), nil
}

// Parse reads an SSE stream and calls fn for every dispatched event. Lines
// longer than maxLineBytes and events whose accumulated data exceeds
// DefaultMaxSSEEventBytes abort the parse. An event without an explicit name
// is reported as "message", matching the SSE specification.
func Parse(r io.Reader, maxLineBytes int, fn func(event, data string) error) error {
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxSSELineBytes
	}
	reader := bufio.NewReaderSize(r, 64<<10)

	var (
		event  string
		data   []string
		size   int
		lineNo int
		buf    []byte
	)
	dispatch := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		name := event
		if name == "" {
			name = "message"
		}
		payload := strings.Join(data, "\n")
		event = ""
		clear(data)
		data = data[:0]
		size = 0
		return fn(name, payload)
	}

	for {
		chunk, readErr := reader.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLineBytes {
			lineNo++
			return fmt.Errorf("sse line %d exceeds %d bytes", lineNo, maxLineBytes)
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}

		lineNo++
		line := strings.TrimSuffix(string(buf), "\n")
		line = strings.TrimSuffix(line, "\r")
		buf = buf[:0]

		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive.
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				data = append(data, value)
				size += len(value) + 1
				if size > DefaultMaxSSEEventBytes {
					lineNo++
					return fmt.Errorf("sse event at line %d exceeds %d bytes", lineNo, DefaultMaxSSEEventBytes)
				}
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return dispatch()
			}
			return readErr
		}
	}
}
