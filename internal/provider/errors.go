// internal/provider/errors.go
package provider

import "errors"

type Class int

const (
	Retryable Class = iota
	Terminal
	Fatal
)

type Error struct {
	Op     string
	Class  Class
	Status int
	Err    error
}

func (e *Error) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

var ErrNotFound = errors.New("provider: instance not found")
var ErrDuplicateLabel = errors.New("provider: duplicate label")
var ErrOutcomeUnknown = errors.New("provider: create outcome unknown")
