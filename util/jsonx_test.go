package util

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestDecodeKeepsNumberPrecision(t *testing.T) {
	t.Parallel()

	value, err := Decode([]byte(`{"temperature":0.30,"max_tokens":12}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	object, ok := AsMap(value)
	if !ok {
		t.Fatalf("expected object, got %T", value)
	}
	number, ok := object["temperature"].(json.Number)
	if !ok || number.String() != "0.30" {
		t.Fatalf("temperature lost precision: %v", object["temperature"])
	}
	if n, ok := AsInt(object["max_tokens"]); !ok || n != 12 {
		t.Fatalf("max_tokens = %d, %v", n, ok)
	}
}

func TestDecodeRejectsTrailingData(t *testing.T) {
	t.Parallel()

	if _, err := Decode([]byte(`{} {}`)); err == nil {
		t.Fatal("expected trailing data error")
	}
	if _, err := Decode([]byte(`{`)); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestMarshalDoesNotEscape(t *testing.T) {
	t.Parallel()

	encoded, err := Marshal(map[string]any{"text": "中文 <b>&</b>"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), `\u`) {
		t.Fatalf("unexpected unicode escape: %s", encoded)
	}
	if strings.Contains(string(encoded), `\u003c`) || strings.Contains(string(encoded), `\u0026`) {
		t.Fatalf("unexpected html escape: %s", encoded)
	}
	if !strings.Contains(string(encoded), "中文 <b>&</b>") {
		t.Fatalf("text not preserved: %s", encoded)
	}
	if strings.HasSuffix(string(encoded), "\n") {
		t.Fatalf("unexpected trailing newline: %q", encoded)
	}
}

func TestAsIntRejectsInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value any
		ok    bool
		want  int
	}{
		{"bool", true, false, 0},
		{"fraction", json.Number("1.5"), false, 0},
		{"float fraction", 2.5, false, 0},
		{"nan", math.NaN(), false, 0},
		{"inf", math.Inf(1), false, 0},
		{"string", "12", false, 0},
		{"number", json.Number("42"), true, 42},
		{"float", 42.0, true, 42},
		{"int", 7, true, 7},
		{"int64", int64(9), true, 9},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := AsInt(test.value)
			if ok != test.ok || got != test.want {
				t.Fatalf("AsInt(%v) = %d, %v; want %d, %v", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestAsFloatRejectsNonFinite(t *testing.T) {
	t.Parallel()

	if _, ok := AsFloat(math.NaN()); ok {
		t.Fatal("NaN must be rejected")
	}
	if _, ok := AsFloat(math.Inf(-1)); ok {
		t.Fatal("Inf must be rejected")
	}
	if f, ok := AsFloat(json.Number("1.25")); !ok || f != 1.25 {
		t.Fatalf("AsFloat = %v, %v", f, ok)
	}
}

func TestOptionalConversions(t *testing.T) {
	t.Parallel()

	if value, ok := AsOptInt(nil); !ok || value != nil {
		t.Fatalf("nil int = %v, %v", value, ok)
	}
	if value, ok := AsOptFloat(3.5); !ok || value == nil || *value != 3.5 {
		t.Fatalf("float = %v, %v", value, ok)
	}
	if _, ok := AsOptInt("x"); ok {
		t.Fatal("invalid optional int must fail")
	}
}

func TestTruthyMatchesPython(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"nil", nil, false},
		{"empty string", "", false},
		{"string", "x", true},
		{"false", false, false},
		{"true", true, true},
		{"empty object", map[string]any{}, false},
		{"object", map[string]any{"a": 1}, true},
		{"empty array", []any{}, false},
		{"array", []any{1}, true},
		{"zero number", json.Number("0"), false},
		{"non-zero number", json.Number("1"), true},
		{"zero float", float64(0), false},
		{"zero int", int64(0), false},
	}
	for _, test := range cases {
		if got := Truthy(test.value); got != test.want {
			t.Errorf("%s: Truthy(%v) = %v, want %v", test.name, test.value, got, test.want)
		}
	}
}
