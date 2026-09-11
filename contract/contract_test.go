package contract_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/pavelpascari/svcrt/contract"
)

// quantityErr is a coded error carrying language-neutral params.
type quantityErr struct{ Max, Got int }

func (e quantityErr) Error() string     { return fmt.Sprintf("quantity %d exceeds %d", e.Got, e.Max) }
func (e quantityErr) ErrorCode() string { return "invalid_quantity" }
func (e quantityErr) ErrorParams() map[string]any {
	return map[string]any{"max": e.Max, "got": e.Got}
}

// notFoundErr is a coded error with no params.
type notFoundErr struct{}

func (notFoundErr) Error() string     { return "not found" }
func (notFoundErr) ErrorCode() string { return "not_found" }

// Compile-time proof that the optional interface is satisfiable and that a
// params-free error is still Coded. This is the discoverability the named
// Detailed interface exists to provide.
var (
	_ contract.Detailed = quantityErr{}
	_ contract.Coded    = notFoundErr{}
	_ contract.APIV1    = contract.APIV1{}
)

func TestDetailedEmbedsCoded(t *testing.T) {
	var d contract.Detailed = quantityErr{Max: 100, Got: 5000}
	var c contract.Coded = d // must compile: Detailed embeds Coded

	if got := c.ErrorCode(); got != "invalid_quantity" {
		t.Errorf("ErrorCode() = %q, want %q", got, "invalid_quantity")
	}
	if got := d.ErrorParams()["max"]; got != 100 {
		t.Errorf("ErrorParams()[max] = %v, want 100", got)
	}
}

// Generated code will find coded errors through wrapping, so this is the
// access pattern that actually matters.
func TestErrorsAsFindsCodedThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("handling request: %w", notFoundErr{})

	var c contract.Coded
	if !errors.As(wrapped, &c) {
		t.Fatalf("errors.As did not find contract.Coded in %v", wrapped)
	}
	if got := c.ErrorCode(); got != "not_found" {
		t.Errorf("ErrorCode() = %q, want %q", got, "not_found")
	}
}

func TestErrorsAsDoesNotFindDetailedOnParamlessError(t *testing.T) {
	wrapped := fmt.Errorf("handling request: %w", notFoundErr{})

	var d contract.Detailed
	if errors.As(wrapped, &d) {
		t.Error("errors.As found Detailed on an error that has no params")
	}
}
