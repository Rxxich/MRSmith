// Package apperr carries errors whose message is safe to show to the user.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func New(status int, format string, a ...any) *Error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, a...)}
}

func Bad(format string, a ...any) error { return New(http.StatusBadRequest, format, a...) }

func NotFound(msg string) *Error { return &Error{Status: http.StatusNotFound, Msg: msg} }

func Conflict(msg string) *Error { return &Error{Status: http.StatusConflict, Msg: msg} }

func TooLarge(msg string) *Error { return &Error{Status: http.StatusRequestEntityTooLarge, Msg: msg} }

// As extracts a user-facing error.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
