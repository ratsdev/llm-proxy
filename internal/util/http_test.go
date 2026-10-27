package util

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadJSONObject(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		body    string
		limit   int64
		want    map[string]any
		wantErr error
	}{
		{
			name:  "object",
			body:  `{"model":"alias"}`,
			limit: 1024,
			want:  map[string]any{"model": "alias"},
		},
		{
			name:    "array",
			body:    `[1,2,3]`,
			limit:   1024,
			wantErr: ErrBodyNotObject,
		},
		{
			name:    "invalid json",
			body:    `{`,
			limit:   1024,
			wantErr: ErrBodyInvalidJSON,
		},
		{
			name:    "trailing data",
			body:    `{"a":1}{"b":2}`,
			limit:   1024,
			wantErr: ErrBodyInvalidJSON,
		},
		{
			name:    "too large",
			body:    `{"padding":"` + strings.Repeat("x", 64) + `"}`,
			limit:   16,
			wantErr: ErrBodyTooLarge,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(testCase.body))
			recorder := httptest.NewRecorder()
			got, err := ReadJSONObject(recorder, request, testCase.limit)

			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("ReadJSONObject(%q) error = %v, want %v", testCase.body, err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadJSONObject(%q) error = %v", testCase.body, err)
			}
			if got["model"] != testCase.want["model"] {
				t.Fatalf("ReadJSONObject(%q) = %v, want %v", testCase.body, got, testCase.want)
			}
		})
	}
}

func TestReadJSONObjectUnreadableBody(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/", errReader{})
	if _, err := ReadJSONObject(httptest.NewRecorder(), request, 1024); !errors.Is(err, ErrBodyUnreadable) {
		t.Fatalf("ReadJSONObject(erroring body) error = %v, want %v", err, ErrBodyUnreadable)
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	WriteJSON(recorder, http.StatusTeapot, map[string]any{"detail": "nope"})

	if recorder.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTeapot)
	}
	if contentType := recorder.Header().Get("content-type"); contentType != "application/json" {
		t.Fatalf("content-type = %q, want application/json", contentType)
	}
	if body := recorder.Body.String(); body != `{"detail":"nope"}` {
		t.Fatalf("body = %q, want compact JSON", body)
	}
}

// WriteJSON cannot encode every Go value; the fallback must still be a coherent
// HTTP response rather than a partially written body.
func TestWriteJSONEncodingFailure(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	WriteJSON(recorder, http.StatusOK, map[string]any{"bad": make(chan int)})

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
