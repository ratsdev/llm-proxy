package util

import (
	"errors"
	"io"
	"net/http"
)

// Request body failures. The gateway API and the console render these as
// different error envelopes, so the reader reports the kind and each surface
// keeps its own status/code mapping.
var (
	// ErrBodyTooLarge reports a body above the configured limit.
	ErrBodyTooLarge = errors.New("request body too large")
	// ErrBodyUnreadable reports a body that could not be read at all.
	ErrBodyUnreadable = errors.New("request body unreadable")
	// ErrBodyInvalidJSON reports a body that is not valid JSON.
	ErrBodyInvalidJSON = errors.New("request body is not valid JSON")
	// ErrBodyNotObject reports a body that is valid JSON but not an object.
	ErrBodyNotObject = errors.New("request body is not a JSON object")
)

// ReadJSONObject reads a request body of at most limit bytes and decodes it as
// a JSON object. The writer is used only by http.MaxBytesReader.
func ReadJSONObject(w http.ResponseWriter, r *http.Request, limit int64) (map[string]any, error) {
	body := http.MaxBytesReader(w, r.Body, limit)
	data, err := io.ReadAll(body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return nil, ErrBodyTooLarge
		}
		return nil, ErrBodyUnreadable
	}
	value, err := Decode(data)
	if err != nil {
		return nil, ErrBodyInvalidJSON
	}
	object, ok := AsMap(value)
	if !ok {
		return nil, ErrBodyNotObject
	}
	return object, nil
}

// WriteJSON writes payload as a compact JSON response body.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	encoded, err := Marshal(payload)
	if err != nil {
		http.Error(w, "Internal server error.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
