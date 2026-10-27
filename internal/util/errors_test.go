package util

import (
	"errors"
	"net/http"
	"testing"
)

func TestNewDefaultsType(t *testing.T) {
	t.Parallel()

	err := New(http.StatusBadRequest, "bad request", "", "invalid_body", nil)
	if err.Type != DefaultErrorType {
		t.Fatalf("New() type = %q, want %q", err.Type, DefaultErrorType)
	}
	if err.Status != http.StatusBadRequest || err.Message != "bad request" || err.Code != "invalid_body" {
		t.Fatalf("New() = %+v", err)
	}
}

func TestErrorImplementsError(t *testing.T) {
	t.Parallel()

	var target error = New(http.StatusBadGateway, "upstream failed", "api_error", "upstream_error", nil)
	if target.Error() != "upstream failed" {
		t.Fatalf("Error() = %q", target.Error())
	}

	// Handlers and the router match gateway errors with errors.As.
	var gatewayErr *Error
	if !errors.As(target, &gatewayErr) || gatewayErr.Code != "upstream_error" {
		t.Fatalf("errors.As() did not recover the gateway error: %+v", gatewayErr)
	}
}
