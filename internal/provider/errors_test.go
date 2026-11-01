// internal/provider/errors_test.go
package provider

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorClassIs(t *testing.T) {
	wrapped := &Error{Op: "get", Class: Terminal, Status: 404, Err: fmt.Errorf("not found: %w", ErrNotFound)}
	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatal("errors.Is did not match ErrNotFound through Error wrapper")
	}
}
