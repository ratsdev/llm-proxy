package util

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
)

// Decode parses exactly one JSON value. Numbers are decoded as json.Number so
// that values forwarded to upstream providers keep their original
// representation.
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return value, nil
	case err != nil:
		return nil, err
	default:
		return nil, errors.New("unexpected data after JSON value")
	}
}

// Marshal renders v as compact JSON without HTML escaping, matching the
// gateway's "emit UTF-8 as-is" contract.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// AsMap asserts v is a JSON object.
func AsMap(v any) (map[string]any, bool) {
	value, ok := v.(map[string]any)
	return value, ok
}

// AsSlice asserts v is a JSON array.
func AsSlice(v any) ([]any, bool) {
	value, ok := v.([]any)
	return value, ok
}

// AsString asserts v is a string.
func AsString(v any) (string, bool) {
	value, ok := v.(string)
	return value, ok
}

// AsBool asserts v is a bool.
func AsBool(v any) (bool, bool) {
	value, ok := v.(bool)
	return value, ok
}

// StringValue returns v when it is a string and the empty string otherwise.
func StringValue(v any) string {
	value, _ := v.(string)
	return value
}

// BoolValue returns v when it is a bool and false otherwise.
func BoolValue(v any) bool {
	value, _ := v.(bool)
	return value
}

// Truthy mirrors Python truthiness for the JSON value types the adapters
// reason about. It exists because several upstream fields are loosely typed
// and the original gateway treats empty containers and zero numbers as absent.
func Truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case map[string]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	case json.Number:
		number, err := typed.Float64()
		return err != nil || number != 0
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	}
	return true
}

// AsInt converts a JSON number to an int. Booleans, fractional values and
// values outside the int range are rejected (Python's bool is not an int for
// the purposes of configuration validation either).
func AsInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return checkedInt(i)
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		i := int64(n)
		if float64(i) != n {
			return 0, false
		}
		return checkedInt(i)
	case int:
		return n, true
	case int64:
		return checkedInt(n)
	}
	return 0, false
}

func checkedInt(v int64) (int, bool) {
	i := int(v)
	return i, int64(i) == v
}

// AsFloat converts a JSON number to a float64, rejecting non-finite values.
func AsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, isFinite(f)
	case float64:
		return n, isFinite(n)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// AsOptInt converts an optional JSON integer. Null and missing values map to
// (nil, true); invalid values map to (nil, false).
func AsOptInt(v any) (*int, bool) {
	if v == nil {
		return nil, true
	}
	i, ok := AsInt(v)
	if !ok {
		return nil, false
	}
	return &i, true
}

// AsOptFloat converts an optional JSON number. Null and missing values map to
// (nil, true); invalid values map to (nil, false).
func AsOptFloat(v any) (*float64, bool) {
	if v == nil {
		return nil, true
	}
	f, ok := AsFloat(v)
	if !ok {
		return nil, false
	}
	return &f, true
}

// Nullable renders an optional value as either its value or JSON null, which is
// how the API represents an unset optional field.
func Nullable[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}
