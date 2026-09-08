package utils

import (
	"errors"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type registration struct {
	Username string `validate:"required"`
	Email    string `validate:"required,email"`
	Password string `validate:"min=8"`
}

func validationErrorFor(t *testing.T, r registration) error {
	t.Helper()
	err := validator.New().Struct(r)
	require.Error(t, err)
	return err
}

func TestNewValidatorErrorNamesEveryOffendingFieldWithItsRule(t *testing.T) {
	err := validationErrorFor(t, registration{Email: "not-an-email", Password: "short"})

	e := NewValidatorError(err)

	assert.Equal(t, "required", e.Errors["Username"])
	assert.Equal(t, "email", e.Errors["Email"])
	assert.Equal(t, "min", e.Errors["Password"])
	assert.Len(t, e.Errors, 3)
}

func TestNewValidatorErrorReportsOnlyTheFieldsThatFailed(t *testing.T) {
	err := validationErrorFor(t, registration{Username: "jake", Email: "jake@jake.jake", Password: "short"})

	e := NewValidatorError(err)

	require.Len(t, e.Errors, 1)
	assert.Equal(t, "min", e.Errors["Password"])
	assert.NotContains(t, e.Errors, "Username")
	assert.NotContains(t, e.Errors, "Email")
}

func TestNewErrorReportsAPlainErrorUnderBody(t *testing.T) {
	e := NewError(errors.New("boom"))

	assert.Equal(t, "boom", e.Errors["body"])
	assert.Len(t, e.Errors, 1)
}
