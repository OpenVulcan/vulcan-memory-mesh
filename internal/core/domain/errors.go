package domain

import (
	"errors"
	"fmt"
)

var (
	ErrValidation = errors.New("validation failed")
	ErrTimeout    = errors.New("request timeout")
)

type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func (e ValidationError) Unwrap() error { return ErrValidation }

func IsValidationError(err error) bool { return errors.Is(err, ErrValidation) }
