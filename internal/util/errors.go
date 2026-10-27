// Package util contains the small shared primitives used by the gateway:
// client-facing errors, JSON helpers, HTTP body helpers, server-sent event
// encoding and identifier generation.
package util

// DefaultErrorType is used when an Error carries no explicit type.
const DefaultErrorType = "invalid_request_error"

// Error is a client-facing gateway error. Handlers translate it into the
// error envelope of the inbound protocol.
type Error struct {
	Status  int
	Message string
	Type    string
	Code    string
	Headers map[string]string
}

// New builds a client-facing error. An empty typ defaults to
// DefaultErrorType.
func New(status int, message, typ, code string, headers map[string]string) *Error {
	if typ == "" {
		typ = DefaultErrorType
	}
	return &Error{
		Status:  status,
		Message: message,
		Type:    typ,
		Code:    code,
		Headers: headers,
	}
}

func (e *Error) Error() string {
	return e.Message
}
