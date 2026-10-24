package util

import (
	"errors"
	"strings"
	"testing"
)

func TestFrame(t *testing.T) {
	t.Parallel()

	encoded, err := Frame("message_start", map[string]any{"type": "message_start"})
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	want := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	if string(encoded) != want {
		t.Fatalf("frame = %q, want %q", encoded, want)
	}

	encoded, err = Frame("", "[DONE]")
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if string(encoded) != "data: [DONE]\n\n" {
		t.Fatalf("done frame = %q", encoded)
	}
}

func TestParseEvents(t *testing.T) {
	t.Parallel()

	stream := strings.Join([]string{
		": keep-alive",
		"event: content_block_delta",
		"data: {\"a\":1}",
		"",
		"data: first",
		"data: second",
		"",
		"data: trailing",
	}, "\r\n")

	type event struct{ name, data string }
	var got []event
	err := Parse(strings.NewReader(stream), 0, func(name, data string) error {
		got = append(got, event{name, data})
		return nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := []event{
		{"content_block_delta", `{"a":1}`},
		{"message", "first\nsecond"},
		{"message", "trailing"},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParsePropagatesCallbackError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("stop")
	err := Parse(strings.NewReader("data: x\n\ndata: y\n\n"), 0, func(string, string) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsLongLine(t *testing.T) {
	t.Parallel()

	stream := "data: " + strings.Repeat("x", 64) + "\n\n"
	err := Parse(strings.NewReader(stream), 16, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsOversizedEvent(t *testing.T) {
	t.Parallel()

	// Many data lines and no terminating blank line: every line fits, so only
	// the per-event budget can stop the parser.
	line := "data: " + strings.Repeat("x", 1<<20) + "\n"
	stream := strings.Repeat(line, DefaultMaxSSEEventBytes/(1<<20)+2)
	err := Parse(strings.NewReader(stream), 0, func(string, string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "sse event") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseEventBudgetResetsBetweenEvents(t *testing.T) {
	t.Parallel()

	// Each event uses half the budget; the second one only parses if the
	// counter is reset after dispatch.
	payload := strings.Repeat("x", DefaultMaxSSEEventBytes/2)
	stream := "data: " + payload + "\n\ndata: " + payload + "\n\n"

	var got int
	err := Parse(strings.NewReader(stream), 0, func(_, data string) error {
		got++
		if data != payload {
			t.Fatalf("event %d payload length = %d", got, len(data))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != 2 {
		t.Fatalf("events = %d, want 2", got)
	}
}

func FuzzParse(f *testing.F) {
	f.Add("data: a\n\n")
	f.Add("event: x\ndata: {\"k\":1}\n\n")
	f.Add(": ping\n\n")
	f.Add("data: a\n")
	f.Add("")

	f.Fuzz(func(t *testing.T, input string) {
		_ = Parse(strings.NewReader(input), 64<<10, func(string, string) error { return nil })
	})
}
