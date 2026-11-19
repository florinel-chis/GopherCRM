package utils

import "context"

// requestIDKey is the context key of the request id; unexported so no other
// package can set or read it except through the functions below.
type requestIDKey struct{}

// WithRequestID returns a copy of ctx that carries the request id, so a
// service can put it on its log lines. The id comes from the client's
// X-Request-ID header unchecked: log it only as a field, never in a message.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext returns the request id WithRequestID stored, or ""
// when there is none.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
