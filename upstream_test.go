package llmproxy

import (
	"strings"
	"testing"
)

func TestReadCapped(t *testing.T) {
	t.Parallel()

	data, exceeded, err := readCapped(strings.NewReader("12345678"), 8)
	if err != nil || exceeded || string(data) != "12345678" {
		t.Fatalf("at limit: data=%q exceeded=%v err=%v", data, exceeded, err)
	}

	data, exceeded, err = readCapped(strings.NewReader("123456789"), 8)
	if err != nil || !exceeded || string(data) != "123456789" {
		t.Fatalf("over limit: data=%q exceeded=%v err=%v", data, exceeded, err)
	}
}
