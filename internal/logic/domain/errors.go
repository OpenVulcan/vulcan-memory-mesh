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

type InvalidLLMOutputError struct {
	Scene   string
	Message string
	Raw     string
}

func (e InvalidLLMOutputError) Error() string {
	if e.Scene == "" {
		return "invalid llm output: " + e.Message
	}
	return fmt.Sprintf("invalid llm output for %s: %s", e.Scene, e.Message)
}
