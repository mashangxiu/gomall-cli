package clierr

import (
	"errors"
	"fmt"
)

const (
	CodeOK           = 0
	CodeInvalidInput = 2
	CodeConfig       = 3
	CodeRuntime      = 10
	CodeInternal     = 11
)

// Error carries both message and process exit code.
type Error struct {
	Code int
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	return fmt.Sprintf("%s: %v", e.Msg, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func New(code int, msg string) error {
	return &Error{Code: code, Msg: msg}
}

func Wrap(code int, msg string, err error) error {
	if err == nil {
		return &Error{Code: code, Msg: msg}
	}
	return &Error{Code: code, Msg: msg, Err: err}
}

// ExitCode maps any error to a stable process exit code.
func ExitCode(err error) int {
	if err == nil {
		return CodeOK
	}
	var cliErr *Error
	if errors.As(err, &cliErr) {
		return cliErr.Code
	}
	return CodeInternal
}
