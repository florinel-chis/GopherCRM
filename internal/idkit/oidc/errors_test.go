package oidc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrors(t *testing.T) {
	cause := errors.New("the cause")
	refusal := &RefusalError{Code: "some_code", Cause: cause}
	assert.Equal(t, "single sign-on failed: some_code", refusal.Error())
	assert.ErrorIs(t, refusal, ErrSignInFailed)
	assert.ErrorIs(t, refusal, cause, "the cause is unwrapped")
	assert.NotErrorIs(t, refusal, ErrUnavailable)
	assert.Nil(t, (&RefusalError{Code: "x"}).Unwrap())

	unavailable := &UnavailableError{Code: "other_code"}
	assert.Equal(t, ErrUnavailable.Error(), unavailable.Error())
	assert.ErrorIs(t, unavailable, ErrUnavailable)
	assert.NotErrorIs(t, unavailable, ErrSignInFailed)

	assert.Equal(t, "some_code", RefusalCode(fmt.Errorf("wrapped: %w", refusal)))
	assert.Equal(t, "other_code", UnavailableCode(fmt.Errorf("wrapped: %w", unavailable)))
	assert.Empty(t, RefusalCode(fmt.Errorf("wrapped: %w", unavailable)))
	assert.Empty(t, UnavailableCode(fmt.Errorf("wrapped: %w", refusal)))
	plain := errors.New("plain")
	assert.Empty(t, RefusalCode(plain))
	assert.Empty(t, UnavailableCode(plain))
	assert.Empty(t, RefusalCode(nil))
}
