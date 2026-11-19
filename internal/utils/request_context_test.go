package utils

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestIDContext(t *testing.T) {
	assert.Equal(t, "", RequestIDFromContext(context.Background()), "no value is the empty string")
	ctx := WithRequestID(context.Background(), "req-1")
	assert.Equal(t, "req-1", RequestIDFromContext(ctx))
	type otherKey string
	shadow := context.WithValue(context.Background(), otherKey("request_id"), "spoof")
	assert.Equal(t, "", RequestIDFromContext(shadow), "only the package's own key counts")
}
